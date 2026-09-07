import Foundation
import Darwin

func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
    if !condition() { throw MaintenanceError("TEST FAILED: " + message) }
}

@main
struct Tests {
    static func main() throws {
        let now = Date(timeIntervalSince1970: 2_000_000_000)
        let old = now.addingTimeInterval(-retentionSeconds - 1)
        try expect(oldEnough(old, now: now), "strictly older than seven days is eligible")
        try expect(!oldEnough(now.addingTimeInterval(-retentionSeconds), now: now), "exact seven-day boundary must be retained")
        try expect(!oldEnough(now, now: now), "new item must be retained")
        try expect(!oldEnough(now.addingTimeInterval(1), now: now), "future date must be retained")
        try expect(!oldEnough(nil, now: now), "unknown date must be retained")
        try expect(!oldEnough(Date(timeIntervalSince1970: .nan), now: now), "invalid date must be retained")

        let manager = FileManager.default
        let fixture = manager.temporaryDirectory.appendingPathComponent("maintenance-fixture-" + UUID().uuidString)
        try manager.createDirectory(at: fixture, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])
        func directory(_ name: String) throws -> URL {
            let path = fixture.appendingPathComponent(name)
            try manager.createDirectory(at: path, withIntermediateDirectories: false,
                                        attributes: [.posixPermissions: 0o700])
            return path
        }
        func file(_ root: URL, _ name: String) throws -> URL {
            let path = root.appendingPathComponent(name)
            try Data("fixture".utf8).write(to: path)
            return path
        }

        let root = try directory("items")
        _ = try file(root, "old")
        _ = try file(root, "new")
        _ = try file(root, "unknown")
        let outside = try file(fixture, "outside")
        try manager.createSymbolicLink(at: root.appendingPathComponent("link"), withDestinationURL: outside)
        let source = try file(root, "hard-source")
        try manager.linkItem(at: source, to: root.appendingPathComponent("hard-link"))
        var removed: [String] = []
        let dates: (URL) throws -> Date? = { item in
            switch item.lastPathComponent {
            case "old": return old
            case "new": return now
            case "unknown": return nil
            default: throw MaintenanceError("Metadata lookup should skip links")
            }
        }
        let dry = try maintain(root: root, now: now, dryRun: true, addedDate: dates,
                               erase: { _, _, _, _, _, _ in throw MaintenanceError("dry run attempted deletion") })
        try expect(dry.eligible == 1 && dry.deleted == 0 && dry.errors == 0, "dry-run selection")
        let selected = try maintain(root: root, now: now, dryRun: false, addedDate: dates,
                                   erase: { _, name, _, _, check, _ in try check(); removed.append(name) })
        try expect(removed == ["old"] && selected.deleted == 1 && selected.errors == 0, "only verified old item selected")

        let changed = try directory("changed-age")
        _ = try file(changed, "candidate")
        var reads = 0
        let rechecked = try maintain(root: changed, now: now, dryRun: false,
                                    addedDate: { _ in reads += 1; return reads == 1 ? old : now },
                                    erase: { _, _, _, _, _, _ in throw MaintenanceError("refreshed age deleted") })
        try expect(rechecked.deleted == 0 && rechecked.skipped == 1, "date refreshed before deletion")

        let returned = try directory("returned-item")
        let returnedFile = try file(returned, "candidate")
        var returnedReads = 0
        let returnedReport = try maintain(root: returned, now: now, dryRun: false,
            addedDate: { _ in returnedReads += 1; return returnedReads <= 2 ? old : now })
        try expect(returnedReport.deleted == 0 && returnedReport.errors == 1,
                   "same inode re-trashed with new date is retained")
        try expect(manager.fileExists(atPath: returnedFile.path), "re-trashed item survives final guard")

        let replaced = try directory("replaced")
        let candidate = try file(replaced, "candidate")
        let replacement = try file(fixture, "replacement")
        var snapshots = 0
        let raced = try maintain(root: replaced, now: now, dryRun: false,
                                addedDate: { _ in
                                    snapshots += 1
                                    if snapshots == 2 {
                                        guard rename(replacement.path, candidate.path) == 0 else {
                                            throw MaintenanceError("fixture file replacement failed")
                                        }
                                    }
                                    return old
                                })
        try expect(raced.deleted == 0 && raced.skipped == 1, "replaced item must not be deleted")
        try expect(manager.fileExists(atPath: candidate.path), "replacement preserved")

        let rootAlias = fixture.appendingPathComponent("root-alias")
        try manager.createSymbolicLink(at: rootAlias, withDestinationURL: root)
        do {
            _ = try maintain(root: rootAlias, now: now, dryRun: true)
            throw MaintenanceError("TEST FAILED: symlink root accepted")
        } catch let error as MaintenanceError {
            try expect(error.description.contains("Cannot open maintenance root"), "symlink root rejected at open")
        }

        // Exercise actual anchored unlink calls on ordinary fixture files only.
        // This session cannot test directory removal against the real OS Trash.
        let actual = try directory("actual-unlink")
        let agedFile = try file(actual, "aged")
        let actualReport = try maintain(root: actual, now: now, dryRun: false, addedDate: { _ in old })
        try expect(actualReport.deleted == 1 && !manager.fileExists(atPath: agedFile.path), "ordinary fixture unlink")
        let childLink = actual.appendingPathComponent("child-link")
        try manager.createSymbolicLink(at: childLink, withDestinationURL: outside)
        let fd = open(actual.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW)
        guard fd >= 0 else { throw MaintenanceError("open fixture") }
        let linkInfo = try entryStat(fd, "child-link")
        try eraseEntry(parent: fd, name: "child-link", expected: Identity(linkInfo), device: linkInfo.st_dev, stillInRoot: {})
        close(fd)
        try expect(manager.fileExists(atPath: outside.path), "link target preserved")
        try expect(!manager.fileExists(atPath: childLink.path), "link entry removed")

        let partial = try directory("partial-root")
        let partialItem = partial.appendingPathComponent("old-directory")
        try manager.createDirectory(at: partialItem, withIntermediateDirectories: false,
                                    attributes: [.posixPermissions: 0o700])
        _ = try file(partialItem, "one")
        _ = try file(partialItem, "two")
        var successfulUnlinks = 0
        let partialReport = try maintain(root: partial, now: now, dryRun: false, addedDate: { _ in old },
            erase: { parent, name, identity, device, check, didRemove in
                try eraseEntry(parent: parent, name: name, expected: identity, device: device,
                    stillInRoot: {
                        try check()
                        if successfulUnlinks > 0 { throw MaintenanceError("injected mid-directory failure") }
                    }, didRemove: { successfulUnlinks += 1; didRemove() })
            })
        try expect(partialReport.deleted == 0 && partialReport.errors == 1,
                   "partially removed directory is not reported complete")
        try expect(partialReport.removedEntries == 1 && partialReport.partiallyDeleted == 1,
                   "partial destructive progress remains visible")
        let remainingPartialEntries = try manager.contentsOfDirectory(atPath: partialItem.path)
        try expect(remainingPartialEntries.count == 1,
                   "partial fixture made exactly one actual removal")

        // Read the actual Foundation date from an ordinary newly created file.
        let native = try directory("native-metadata")
        let recent = try file(native, "recent")
        let nativeDate = try recent.resourceValues(forKeys: [.addedToDirectoryDateKey]).addedToDirectoryDate
        if let nativeDate {
            try expect(abs(nativeDate.timeIntervalSinceNow) < 120, "native date-added reflects fixture creation")
        }
        let nativeReport = try maintain(root: native, now: Date(), dryRun: true)
        try expect(nativeReport.eligible == 0, "native newly added file retained")

        print("Maintenance tests passed: age boundaries, unknown ages, dry run, link handling, identity replacement, native metadata, fixture unlink, partial cleanup reporting.")
        print("Retained ordinary fixture directory: \(fixture.path)")
    }
}
