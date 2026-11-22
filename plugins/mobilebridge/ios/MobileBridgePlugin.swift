import Foundation
import UIKit

/// MyGo native iOS plugin bridging the shared App Group outbox (populated by
/// TailChat's pure-Swift ShareExtension) and UIApplication background tasks
/// while file transfers are active.
public final class MobileBridgePlugin: MyGoPlugin {
    private static let pluginID = "mobilebridge"
    private static let appGroupID = "group.dev.tailchat.shared"

    private var hasActiveTransfers = false
    private var bgTaskID: UIBackgroundTaskIdentifier = .invalid
    private var observers: [NSObjectProtocol] = []

    public init() {
        let nc = NotificationCenter.default
        observers.append(
            nc.addObserver(
                forName: UIApplication.didEnterBackgroundNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                self?.onDidEnterBackground()
            }
        )
        observers.append(
            nc.addObserver(
                forName: UIApplication.willEnterForegroundNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                self?.endBackgroundTaskIfNeeded()
            }
        )
    }

    deinit {
        let nc = NotificationCenter.default
        for token in observers {
            nc.removeObserver(token)
        }
        endBackgroundTaskIfNeeded()
    }

    public func call(_ method: String, _ args: [String: Any], _ reply: @escaping PluginReply) {
        switch method {
        case "drainOutbox":
            let items = drainAppGroupOutbox()
            reply(.success(items))

        case "setActiveTransfers":
            let active = (args["active"] as? Bool) ?? false
            hasActiveTransfers = active
            if !active {
                endBackgroundTaskIfNeeded()
            }
            reply(.success(nil))

        case "startForeground", "updateForeground", "stopForeground":
            // Android-specific foreground notification methods are no-ops on iOS.
            reply(.success(nil))

        default:
            reply(.failure(PluginFailure("UnknownMethod", method)))
        }
    }

    private func onDidEnterBackground() {
        guard hasActiveTransfers, bgTaskID == .invalid else { return }
        bgTaskID = UIApplication.shared.beginBackgroundTask(withName: "dev.tailchat.transfer") { [weak self] in
            self?.endBackgroundTaskIfNeeded()
        }
    }

    private func endBackgroundTaskIfNeeded() {
        guard bgTaskID != .invalid else { return }
        let taskID = bgTaskID
        bgTaskID = .invalid
        UIApplication.shared.endBackgroundTask(taskID)
    }

    /// Reads outbox/<uuid>/manifest.json entries written by ShareExtension,
    /// copies staged files into the app's local caches directory so they outlive
    /// outbox cleanup, removes the consumed outbox directories, and returns the
    /// SharePayload array to Go.
    private func drainAppGroupOutbox() -> [[String: Any]] {
        let fm = FileManager.default
        guard let containerURL = fm.containerURL(
            forSecurityApplicationGroupIdentifier: Self.appGroupID
        ) else {
            return []
        }

        let outboxURL = containerURL.appendingPathComponent("outbox", isDirectory: true)
        guard let subdirs = try? fm.contentsOfDirectory(
            at: outboxURL,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ) else {
            return []
        }

        let stagingRoot = fm.temporaryDirectory.appendingPathComponent("tailchat-inbox", isDirectory: true)
        try? fm.createDirectory(at: stagingRoot, withIntermediateDirectories: true)

        var results: [[String: Any]] = []
        for dir in subdirs.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
            let manifestURL = dir.appendingPathComponent("manifest.json")
            guard let data = try? Data(contentsOf: manifestURL),
                  let manifest = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                continue
            }

            let itemID = (manifest["id"] as? String) ?? dir.lastPathComponent
            let text = (manifest["text"] as? String) ?? ""
            let relFiles = (manifest["files"] as? [String]) ?? []

            var localFiles: [String] = []
            if !relFiles.isEmpty {
                let itemStageDir = stagingRoot.appendingPathComponent(itemID, isDirectory: true)
                try? fm.createDirectory(at: itemStageDir, withIntermediateDirectories: true)
                for rel in relFiles {
                    let safeName = (rel as NSString).lastPathComponent
                    guard !safeName.isEmpty, safeName != ".", safeName != ".." else { continue }
                    let src = dir.appendingPathComponent(safeName)
                    let dst = itemStageDir.appendingPathComponent(safeName)
                    if fm.fileExists(atPath: src.path) {
                        try? fm.removeItem(at: dst)
                        if (try? fm.copyItem(at: src, to: dst)) != nil {
                            localFiles.append(dst.path)
                        }
                    }
                }
            }

            var payload: [String: Any] = [
                "id": itemID,
                "files": localFiles,
                "source": "ios_share_extension"
            ]
            if !text.isEmpty {
                payload["text"] = text
            }
            results.append(payload)

            try? fm.removeItem(at: dir)
        }
        return results
    }
}
