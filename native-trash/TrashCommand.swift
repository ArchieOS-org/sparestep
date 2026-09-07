import Foundation
import Darwin

enum NativeTrashError: Error, CustomStringConvertible {
    case invalid(String)
    case operationUnknown(TrashReceipt)

    var description: String {
        switch self {
        case let .invalid(message): return message
        case let .operationUnknown(receipt):
            return "operation reached state " + receipt.state + ", but receipt finalization failed; see recovery JSON below and do not retry"
        }
    }
}

struct TrashReceipt: Codable, Equatable {
    let version: Int
    var state: String
    let originalPath: String
    var trashedPath: String?

    enum CodingKeys: String, CodingKey {
        case version
        case state
        case originalPath = "original_path"
        case trashedPath = "trashed_path"
    }
}

typealias TrashMover = (URL) throws -> URL
typealias ItemMover = (URL, URL) throws -> Void
typealias ReceiptWriter = (URL, TrashReceipt) throws -> Void

private func lstatInfo(_ path: String) throws -> stat {
    var info = stat()
    guard lstat(path, &info) == 0 else {
        throw NativeTrashError.invalid("cannot inspect path")
    }
    return info
}

private func isDirectory(_ info: stat) -> Bool {
    (info.st_mode & S_IFMT) == S_IFDIR
}

private func normalizedAbsoluteComponents(_ path: String) throws -> [String] {
    guard path.hasPrefix("/") else {
        throw NativeTrashError.invalid("path must be absolute")
    }
    let components = path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
    guard !components.contains(where: { $0 == "." || $0 == ".." || $0.isEmpty }) else {
        throw NativeTrashError.invalid("path must not contain traversal components")
    }
    return components
}

private func isProhibitedComponent(_ component: String) -> Bool {
    component.caseInsensitiveCompare(".trash") == .orderedSame ||
        component.caseInsensitiveCompare(".trashes") == .orderedSame
}

private func pathFromComponents(_ components: [String]) -> String {
    components.isEmpty ? "/" : "/" + components.joined(separator: "/")
}

// Resolve ancestors without following the last item. This preserves a symlink
// source as a symlink while preventing an ancestor alias from entering Trash.
private func safeResolvedParent(_ absolutePath: String) throws -> String {
    let initial = try normalizedAbsoluteComponents(absolutePath)
    guard !initial.isEmpty else {
        throw NativeTrashError.invalid("filesystem root cannot be moved")
    }
    var resolved: [String] = []
    var pending = Array(initial.dropLast())
    var symlinkHops = 0

    while !pending.isEmpty {
        let part = pending.removeFirst()
        if part == "." { continue }
        if part == ".." {
            guard !resolved.isEmpty else {
                throw NativeTrashError.invalid("path escapes filesystem root")
            }
            resolved.removeLast()
            continue
        }
        guard !isProhibitedComponent(part) else {
            throw NativeTrashError.invalid("Trash paths cannot be moved or used as ancestors")
        }
        let candidate = pathFromComponents(resolved + [part])
        let info = try lstatInfo(candidate)
        if (info.st_mode & S_IFMT) == S_IFLNK {
            symlinkHops += 1
            guard symlinkHops <= 32 else {
                throw NativeTrashError.invalid("too many symlink ancestors")
            }
            let target: String
            do {
                target = try FileManager.default.destinationOfSymbolicLink(atPath: candidate)
            } catch {
                throw NativeTrashError.invalid("cannot inspect symlink ancestor")
            }
            let targetParts = target.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
            if target.hasPrefix("/") { resolved.removeAll() }
            pending = targetParts + pending
            continue
        }
        guard isDirectory(info) else {
            throw NativeTrashError.invalid("path ancestor is not a directory")
        }
        resolved.append(part)
    }
    return pathFromComponents(resolved)
}

private func safeExistingSource(_ path: String) throws -> String {
    let initial = try normalizedAbsoluteComponents(path)
    guard let name = initial.last, !isProhibitedComponent(name) else {
        throw NativeTrashError.invalid("filesystem root or Trash path cannot be moved")
    }
    let parent = try safeResolvedParent(path)
    let resolved = pathFromComponents(parent == "/" ? [name] : parent.split(separator: "/").map(String.init) + [name])
    _ = try lstatInfo(resolved) // lstat deliberately preserves a final symlink.
    return resolved
}

private func isSameOrAncestor(_ possibleAncestor: String, of path: String) -> Bool {
    possibleAncestor == "/" || path == possibleAncestor || path.hasPrefix(possibleAncestor + "/")
}

private func protectedSourcePaths() -> [String] {
    let home = FileManager.default.homeDirectoryForCurrentUser.path
    let cwd = FileManager.default.currentDirectoryPath
    let resolved = [home, cwd].compactMap { candidate -> String? in
        // Appending a harmless final component makes the protected directory an
        // ancestor, so safeResolvedParent resolves it without opening Trash.
        try? safeResolvedParent(candidate + "/.agent-native-trash-protected")
    }
    return ["/"] + resolved
}

private func rejectProtectedSource(_ source: String) throws {
    for protected in protectedSourcePaths() {
        guard let components = try? normalizedAbsoluteComponents(protected) else { continue }
        let lexical = pathFromComponents(components)
        if isSameOrAncestor(source, of: lexical) {
            throw NativeTrashError.invalid("refusing to move filesystem root, home, working directory, or an ancestor")
        }
    }
}

private func validateReceiptLocation(_ receiptURL: URL, allowExisting: Bool) throws {
    let receiptPath = receiptURL.path
    _ = try normalizedAbsoluteComponents(receiptPath)
    let parent = try safeResolvedParent(receiptPath)
    let parentInfo = try lstatInfo(parent)
    guard isDirectory(parentInfo), parentInfo.st_uid == geteuid(), parentInfo.st_mode & 0o077 == 0 else {
        throw NativeTrashError.invalid("receipt parent must be a private directory owned by this user")
    }
    var info = stat()
    if lstat(receiptPath, &info) == 0 {
        guard allowExisting, (info.st_mode & S_IFMT) == S_IFREG, info.st_uid == geteuid(), info.st_mode & 0o077 == 0 else {
            throw NativeTrashError.invalid("receipt target is not a private regular file")
        }
    } else if errno != ENOENT {
        throw NativeTrashError.invalid("cannot inspect receipt target")
    } else if allowExisting {
        throw NativeTrashError.invalid("receipt does not exist")
    }
}

func writeReceipt(_ receiptURL: URL, _ receipt: TrashReceipt) throws {
    try validateReceiptLocation(receiptURL, allowExisting: receipt.state != "pending")
    let data = try JSONEncoder().encode(receipt)
    try data.write(to: receiptURL, options: .atomic)
    let permissions: NSNumber = 0o600
    try FileManager.default.setAttributes([.posixPermissions: permissions], ofItemAtPath: receiptURL.path)
}

func readReceipt(_ receiptURL: URL) throws -> TrashReceipt {
    try validateReceiptLocation(receiptURL, allowExisting: true)
    let data = try Data(contentsOf: receiptURL, options: .mappedIfSafe)
    let receipt = try JSONDecoder().decode(TrashReceipt.self, from: data)
    guard receipt.version == 1 else {
        throw NativeTrashError.invalid("receipt version is not supported")
    }
    return receipt
}

func isExpectedNativeTrashItem(_ path: String) -> Bool {
    guard let components = try? normalizedAbsoluteComponents(path), !components.isEmpty else { return false }
    let lower = components.map { $0.lowercased() }
    guard let trashIndex = lower.firstIndex(where: { $0 == ".trash" || $0 == ".trashes" }) else { return false }
    // A valid result is an item below one native Trash root, never the root
    // itself and never a path with an additional Trash component.
    guard !lower[(trashIndex + 1)...].contains(where: { $0 == ".trash" || $0 == ".trashes" }) else { return false }
    let after = Array(components.dropFirst(trashIndex + 1))
    if lower[trashIndex] == ".trash" {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        guard let homeParts = try? normalizedAbsoluteComponents(home),
              Array(lower.prefix(trashIndex)) == homeParts.map({ $0.lowercased() }) else { return false }
        return after.count >= 1
    }
    // External-volume Trash locations use .Trashes/<uid>/<item>.
    return after.count >= 2 && after[0] == String(geteuid())
}

private func nativeTrashMove(_ source: URL) throws -> URL {
    var result: NSURL?
    try FileManager.default.trashItem(at: source, resultingItemURL: &result)
    guard let result else {
        throw NativeTrashError.invalid("macOS did not report the Trash destination")
    }
    return result as URL
}

func moveToTrash(receiptURL: URL, sourcePath: String,
                 trash: TrashMover = nativeTrashMove,
                 saveReceipt: ReceiptWriter = writeReceipt,
                 expectedTrashItem: (String) -> Bool = isExpectedNativeTrashItem) throws -> TrashReceipt {
    try validateReceiptLocation(receiptURL, allowExisting: false)
    let source = try safeExistingSource(sourcePath)
    try rejectProtectedSource(source)
    var receipt = TrashReceipt(version: 1, state: "pending", originalPath: source, trashedPath: nil)
    try saveReceipt(receiptURL, receipt)
    let trashed = try trash(URL(fileURLWithPath: source)).path
    guard expectedTrashItem(trashed) else {
        throw NativeTrashError.invalid("macOS returned an invalid Trash destination")
    }
    receipt.state = "moved"
    receipt.trashedPath = trashed
    do {
        try saveReceipt(receiptURL, receipt)
    } catch {
        throw NativeTrashError.operationUnknown(receipt)
    }
    return receipt
}

func restoreFromTrash(receiptURL: URL,
                      move: ItemMover = { try FileManager.default.moveItem(at: $0, to: $1) },
                      saveReceipt: ReceiptWriter = writeReceipt,
                      destinationExists: (String) throws -> Bool = { path in
                          var info = stat()
                          if lstat(path, &info) == 0 { return true }
                          if errno == ENOENT { return false }
                          throw NativeTrashError.invalid("cannot inspect restore destination")
                      },
                      expectedTrashItem: (String) -> Bool = isExpectedNativeTrashItem) throws -> TrashReceipt {
    var receipt = try readReceipt(receiptURL)
    guard receipt.state == "moved", let trashedPath = receipt.trashedPath,
          expectedTrashItem(trashedPath) else {
        throw NativeTrashError.invalid("receipt is not a restorable native Trash move")
    }
    let original = try safeExistingSourcePathForRestore(receipt.originalPath)
    if try destinationExists(original) {
        throw NativeTrashError.invalid("restore destination already exists")
    }
    try move(URL(fileURLWithPath: trashedPath), URL(fileURLWithPath: original))
    receipt.state = "restored"
    do {
        try saveReceipt(receiptURL, receipt)
    } catch {
        throw NativeTrashError.operationUnknown(receipt)
    }
    return receipt
}

private func safeExistingSourcePathForRestore(_ path: String) throws -> String {
    let initial = try normalizedAbsoluteComponents(path)
    guard let name = initial.last, !isProhibitedComponent(name) else {
        throw NativeTrashError.invalid("receipt has an unsafe original path")
    }
    let parent = try safeResolvedParent(path)
    let resolved = pathFromComponents(parent == "/" ? [name] : parent.split(separator: "/").map(String.init) + [name])
    try rejectProtectedSource(resolved)
    return resolved
}

private func emitJSON<T: Encodable>(_ value: T, to handle: FileHandle = .standardOutput) throws {
    let data = try JSONEncoder().encode(value)
    handle.write(data)
    handle.write(Data("\n".utf8))
}

func runNativeTrashCommand(_ arguments: [String]) throws {
    if arguments.count == 5, arguments[0] == "move", arguments[1] == "--receipt", arguments[3] == "--" {
        let receipt = arguments[2]
        let source = arguments[4]
        try emitJSON(moveToTrash(receiptURL: URL(fileURLWithPath: receipt), sourcePath: source))
    } else if arguments.count == 3, arguments[0] == "restore", arguments[1] == "--receipt" {
        let receipt = arguments[2]
        try emitJSON(restoreFromTrash(receiptURL: URL(fileURLWithPath: receipt)))
    } else {
        throw NativeTrashError.invalid("usage: agent-native-trash move --receipt /absolute/receipt.json -- /absolute/source | restore --receipt /absolute/receipt.json")
    }
}

#if !TESTING
@main
struct AgentNativeTrash {
    static func main() {
        do {
            try runNativeTrashCommand(Array(CommandLine.arguments.dropFirst()))
        } catch let error as NativeTrashError {
            if case let .operationUnknown(receipt) = error {
                try? emitJSON(receipt, to: .standardError)
            }
            fputs("agent-native-trash: \(error.description)\n", stderr)
            exit(1)
        } catch {
            fputs("agent-native-trash: \(error)\n", stderr)
            exit(1)
        }
    }
}
#endif
