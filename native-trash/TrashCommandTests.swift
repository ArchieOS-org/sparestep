import Foundation
import Darwin

func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
    if !condition() { throw NativeTrashError.invalid("TEST FAILED: " + message) }
}

@main
struct NativeTrashCommandTests {
    static func main() throws {
        let manager = FileManager.default
        let fixture = manager.temporaryDirectory.appendingPathComponent("native-trash-fixture-" + UUID().uuidString)
        try manager.createDirectory(at: fixture, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])
        let receipts = fixture.appendingPathComponent("receipts", isDirectory: true)
        let bin = fixture.appendingPathComponent("fixture-bin", isDirectory: true)
        try manager.createDirectory(at: receipts, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])
        try manager.createDirectory(at: bin, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])

        func file(_ parent: URL, _ name: String, _ contents: String = "fixture") throws -> URL {
            let item = parent.appendingPathComponent(name)
            try Data(contents.utf8).write(to: item, options: .withoutOverwriting)
            return item
        }
        func receipt(_ name: String) -> URL { receipts.appendingPathComponent(name + ".json") }
        let physicalBin = bin.resolvingSymlinksInPath()
        let expectedFixtureTrash: (String) -> Bool = { path in
            path.hasPrefix(physicalBin.path + "/")
        }
        var observedKinds: [String: mode_t] = [:]
        func fixtureTrash(_ source: URL) throws -> URL {
            var candidate = physicalBin.appendingPathComponent(source.lastPathComponent)
            var suffix = 1
            while observedKinds[candidate.path] != nil {
                candidate = physicalBin.appendingPathComponent(source.lastPathComponent + " " + String(suffix))
                suffix += 1
            }
            observedKinds[candidate.path] = try lstatForTest(source.path).st_mode & S_IFMT
            return candidate
        }

        // A file move records the actual collision-adjusted destination and can
        // be restored once without overwriting a new destination.
        let first = try file(fixture, "same")
        let firstReceipt = try moveToTrash(receiptURL: receipt("first"), sourcePath: first.path,
                                           trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
        try expect(firstReceipt.state == "moved" && firstReceipt.trashedPath?.hasSuffix("/same") == true,
                   "file move stores native destination")
        try expect(observedKinds[firstReceipt.trashedPath!] == S_IFREG, "file mover receives a regular file")
        let secondParent = fixture.appendingPathComponent("second", isDirectory: true)
        try manager.createDirectory(at: secondParent, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])
        let second = try file(secondParent, "same")
        let secondReceipt = try moveToTrash(receiptURL: receipt("second"), sourcePath: second.path,
                                            trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
        try expect(secondReceipt.trashedPath?.hasSuffix("/same 1") == true,
                   "collision destination is preserved in receipt")
        var restoreCall: (URL, URL)?
        let restored = try restoreFromTrash(receiptURL: receipt("first"), move: { source, destination in
            restoreCall = (source, destination)
        }, destinationExists: { _ in false }, expectedTrashItem: expectedFixtureTrash)
        try expect(restored.state == "restored" && restoreCall?.1.path == firstReceipt.originalPath, "file restore succeeds")
        do {
            _ = try restoreFromTrash(receiptURL: receipt("first"), move: { _, _ in }, destinationExists: { _ in false },
                                     expectedTrashItem: expectedFixtureTrash)
            throw NativeTrashError.invalid("TEST FAILED: restored receipt accepted twice")
        } catch let error as NativeTrashError {
            try expect(error.description.contains("restorable"), "repeat restore is rejected")
        }

        // Directories retain their nested content and a symlink moves as the
        // link entry, never as its referent.
        let directory = fixture.appendingPathComponent("bundle", isDirectory: true)
        try manager.createDirectory(at: directory.appendingPathComponent("nested", isDirectory: true),
                                    withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        _ = try file(directory.appendingPathComponent("nested"), "proof", "directory payload")
        let directoryReceipt = try moveToTrash(receiptURL: receipt("directory"), sourcePath: directory.path,
                                               trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
        try expect(observedKinds[directoryReceipt.trashedPath!] == S_IFDIR, "directory mover receives the directory entry")
        let referent = try file(fixture, "referent", "must remain")
        let link = fixture.appendingPathComponent("link")
        try manager.createSymbolicLink(at: link, withDestinationURL: referent)
        let linkReceipt = try moveToTrash(receiptURL: receipt("link"), sourcePath: link.path,
                                          trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
        try expect(observedKinds[linkReceipt.trashedPath!] == S_IFLNK, "symlink entry moved without following it")
        try expect(manager.fileExists(atPath: referent.path), "symlink referent remains")

        // Existing receipts do not get overwritten, and a failure finalizing a
        // receipt after a move remains an explicit unknown outcome.
        let protectedReceipt = receipt("already-present")
        try Data("existing".utf8).write(to: protectedReceipt)
        let guardedSource = try file(fixture, "guarded")
        do {
            _ = try moveToTrash(receiptURL: protectedReceipt, sourcePath: guardedSource.path,
                                trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
            throw NativeTrashError.invalid("TEST FAILED: existing receipt was accepted")
        } catch let error as NativeTrashError {
            try expect(error.description.contains("receipt target"), "receipt collision is rejected")
        }
        try expect(manager.fileExists(atPath: guardedSource.path), "receipt collision leaves source alone")
        let partialSource = try file(fixture, "partial")
        var saves = 0
        do {
            _ = try moveToTrash(receiptURL: receipt("partial"), sourcePath: partialSource.path,
                                trash: fixtureTrash,
                                saveReceipt: { path, entry in
                                    saves += 1
                                    if saves == 2 { throw NativeTrashError.invalid("injected receipt finalization failure") }
                                    try writeReceipt(path, entry)
                                }, expectedTrashItem: expectedFixtureTrash)
            throw NativeTrashError.invalid("TEST FAILED: finalization failure was reported successful")
        } catch let error as NativeTrashError {
            guard case let .operationUnknown(recovery) = error else { throw error }
            try expect(recovery.state == "moved" && recovery.trashedPath != nil, "unknown outcome has exact recovery path")
        }
        try expect(observedKinds.values.contains(S_IFREG), "partial failure occurs after a fixture move was attempted")
        let pendingReceipt = try readReceipt(receipt("partial"))
        try expect(pendingReceipt.state == "pending", "durable pending receipt is retained")

        // Restore has a no-overwrite precondition, even when the mover would
        // otherwise be capable of replacing the destination.
        let collisionSource = try file(fixture, "restore-collision", "old")
        _ = try moveToTrash(receiptURL: receipt("restore-collision"), sourcePath: collisionSource.path,
                            trash: fixtureTrash, expectedTrashItem: expectedFixtureTrash)
        do {
            _ = try restoreFromTrash(receiptURL: receipt("restore-collision"), move: { _, _ in
                throw NativeTrashError.invalid("mover should not run when destination exists")
            }, destinationExists: { _ in true }, expectedTrashItem: expectedFixtureTrash)
            throw NativeTrashError.invalid("TEST FAILED: restore overwrote destination")
        } catch let error as NativeTrashError {
            try expect(error.description.contains("already exists"), "restore destination collision rejected")
        }

        // This is lexical only: it does not open or enumerate any real Trash.
        let homeTrashShape = FileManager.default.homeDirectoryForCurrentUser.path + "/.Trash/fixture"
        try expect(isExpectedNativeTrashItem(homeTrashShape), "home Trash receipt shape accepted lexically")
        try expect(!isExpectedNativeTrashItem("/tmp/not-a-trash/fixture"), "non-Trash receipt shape rejected")
        do {
            _ = try moveToTrash(receiptURL: receipt("root"), sourcePath: "/", trash: fixtureTrash,
                                expectedTrashItem: expectedFixtureTrash)
            throw NativeTrashError.invalid("TEST FAILED: root was accepted")
        } catch { /* expected; no native move was attempted */ }

        print("Native Trash tests passed: file and collision receipts, directory and symlink moves, no-overwrite restore, protected inputs, and post-move receipt failure recovery.")
    }

    private static func lstatForTest(_ path: String) throws -> stat {
        var info = stat()
        guard lstat(path, &info) == 0 else { throw NativeTrashError.invalid("cannot inspect fixture") }
        return info
    }
}
