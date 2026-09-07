import Foundation
import Darwin

func runMaintenance() throws -> Int32 {
    let arguments = Array(CommandLine.arguments.dropFirst())
    if arguments == ["--help"] || arguments.isEmpty {
        print("trash-maintenance --run | --dry-run\nmacOS home Trash only; strictly more than seven days since date added.")
        return 0
    }
    guard arguments == ["--run"] || arguments == ["--dry-run"] else {
        throw MaintenanceError("Expected --run or --dry-run; arbitrary cleanup paths are not accepted")
    }
    guard getuid() == geteuid(), getuid() != 0, let account = getpwuid(getuid()),
          let rawHome = account.pointee.pw_dir else {
        throw MaintenanceError("Run as a normal logged-in macOS user")
    }
    let home = URL(fileURLWithPath: String(cString: rawHome), isDirectory: true)
    let state = home.appendingPathComponent("Library/Application Support/one-shot-trash-maintenance", isDirectory: true)
    try FileManager.default.createDirectory(at: state, withIntermediateDirectories: true,
                                          attributes: [.posixPermissions: 0o700])
    let lock = open(state.appendingPathComponent("worker.lock").path,
                    O_CREAT | O_RDWR | O_NOFOLLOW | O_CLOEXEC, 0o600)
    guard lock >= 0 else { throw MaintenanceError("Cannot open worker lock") }
    defer { close(lock) }
    guard flock(lock, LOCK_EX | LOCK_NB) == 0 else {
        if errno == EWOULDBLOCK { return 0 }
        throw MaintenanceError("Cannot acquire worker lock")
    }
    let report = try maintain(root: home.appendingPathComponent(".Trash", isDirectory: true),
                              now: Date(), dryRun: arguments == ["--dry-run"])
    let encoder = JSONEncoder()
    encoder.dateEncodingStrategy = .iso8601
    let encoded = try encoder.encode(report)
    print(String(decoding: encoded, as: UTF8.self))
    return report.errors == 0 ? 0 : 1
}

do {
    exit(try runMaintenance())
} catch {
    fputs("trash-maintenance: \(error)\n", stderr)
    exit(1)
}
