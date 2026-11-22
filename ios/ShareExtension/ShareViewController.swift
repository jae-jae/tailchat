import Foundation
import UIKit
import UniformTypeIdentifiers

/// Pure Swift, zero-Go-runtime iOS Share Extension for TailChat.
///
/// iOS caps Share Extensions at ~120 MB of RAM, so this target never links Go or Tailcat.
/// Instead it streams shared text, URLs, images, and files directly into the shared App Group
/// container (`group.dev.tailchat.shared/outbox/<uuid>/manifest.json`) and lets the user
/// either open TailChat immediately ("Send Now") or queue items for the next app launch.
final class ShareViewController: UIViewController {
    private static let appGroupID = "group.dev.tailchat.shared"
    private static let flushURL = URL(string: "tailchat://flush-outbox")!

    private let titleLabel = UILabel()
    private let statusLabel = UILabel()
    private let noteField = UITextView()
    private let sendNowButton = UIButton(type: .system)
    private let queueButton = UIButton(type: .system)
    private let cancelButton = UIButton(type: .system)

    private var stagedDir: URL?
    private var stagedFiles: [String] = []
    private var extractedTexts: [String] = []
    private var stagingComplete = false

    override func viewDidLoad() {
        super.viewDidLoad()
        setupUI()
        stageIncomingItems()
    }

    private func setupUI() {
        view.backgroundColor = .systemBackground

        let card = UIStackView()
        card.axis = .vertical
        card.spacing = 12
        card.translatesAutoresizingMaskIntoConstraints = false
        view.addSubview(card)

        NSLayoutConstraint.activate([
            card.leadingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.leadingAnchor, constant: 20),
            card.trailingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.trailingAnchor, constant: -20),
            card.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor, constant: 20),
        ])

        titleLabel.text = "Share to TailChat"
        titleLabel.font = .boldSystemFont(ofSize: 20)

        statusLabel.text = "Preparing shared items..."
        statusLabel.font = .systemFont(ofSize: 13)
        statusLabel.textColor = .secondaryLabel
        statusLabel.numberOfLines = 0

        noteField.font = .systemFont(ofSize: 15)
        noteField.layer.borderWidth = 1
        noteField.layer.borderColor = UIColor.separator.cgColor
        noteField.layer.cornerRadius = 8
        noteField.heightAnchor.constraint(equalToConstant: 96).isActive = true

        sendNowButton.setTitle("Send Now", for: .normal)
        sendNowButton.titleLabel?.font = .boldSystemFont(ofSize: 16)
        sendNowButton.backgroundColor = .systemBlue
        sendNowButton.setTitleColor(.white, for: .normal)
        sendNowButton.layer.cornerRadius = 10
        sendNowButton.heightAnchor.constraint(equalToConstant: 44).isActive = true
        sendNowButton.isEnabled = false
        sendNowButton.addTarget(self, action: #selector(onSendNow), for: .touchUpInside)

        queueButton.setTitle("Queue for Next Open", for: .normal)
        queueButton.titleLabel?.font = .systemFont(ofSize: 15, weight: .medium)
        queueButton.backgroundColor = .secondarySystemBackground
        queueButton.layer.cornerRadius = 10
        queueButton.heightAnchor.constraint(equalToConstant: 42).isActive = true
        queueButton.isEnabled = false
        queueButton.addTarget(self, action: #selector(onQueue), for: .touchUpInside)

        cancelButton.setTitle("Cancel", for: .normal)
        cancelButton.addTarget(self, action: #selector(onCancel), for: .touchUpInside)

        card.addArrangedSubview(titleLabel)
        card.addArrangedSubview(statusLabel)
        card.addArrangedSubview(noteField)
        card.addArrangedSubview(sendNowButton)
        card.addArrangedSubview(queueButton)
        card.addArrangedSubview(cancelButton)
    }

    private func stageIncomingItems() {
        guard let container = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: Self.appGroupID
        ) else {
            statusLabel.text = "App Group container unavailable. Check entitlements."
            return
        }

        let itemID = UUID().uuidString
        let itemDir = container
            .appendingPathComponent("outbox", isDirectory: true)
            .appendingPathComponent(itemID, isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: itemDir, withIntermediateDirectories: true)
        } catch {
            statusLabel.text = "Failed to create outbox directory: \(error.localizedDescription)"
            return
        }
        stagedDir = itemDir

        let items = (extensionContext?.inputItems as? [NSExtensionItem]) ?? []
        let group = DispatchGroup()
        let syncQueue = DispatchQueue(label: "dev.tailchat.share.stage")

        for item in items {
            if let attr = item.attributedContentText?.string.trimmingCharacters(in: .whitespacesAndNewlines),
               !attr.isEmpty {
                syncQueue.sync { self.extractedTexts.append(attr) }
            }
            for provider in (item.attachments ?? []) {
                group.enter()
                extractProvider(provider, into: itemDir, syncQueue: syncQueue) {
                    group.leave()
                }
            }
        }

        group.notify(queue: .main) { [weak self] in
            guard let self else { return }
            self.stagingComplete = true
            let combinedText = self.extractedTexts.joined(separator: "\n")
            if !combinedText.isEmpty {
                self.noteField.text = combinedText
            }
            let fileSummary = self.stagedFiles.isEmpty
                ? "Text ready to send"
                : "\(self.stagedFiles.count) file(s) staged: \(self.stagedFiles.joined(separator: ", "))"
            self.statusLabel.text = fileSummary
            self.sendNowButton.isEnabled = true
            self.queueButton.isEnabled = true
        }
    }

    private func extractProvider(
        _ provider: NSItemProvider,
        into itemDir: URL,
        syncQueue: DispatchQueue,
        done: @escaping () -> Void
    ) {
        if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
            provider.loadFileRepresentation(forTypeIdentifier: UTType.item.identifier) { url, _ in
                if let url {
                    self.copyFile(from: url, into: itemDir, syncQueue: syncQueue)
                }
                done()
            }
            return
        }

        if provider.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
            provider.loadFileRepresentation(forTypeIdentifier: UTType.image.identifier) { url, _ in
                if let url {
                    self.copyFile(from: url, into: itemDir, syncQueue: syncQueue)
                }
                done()
            }
            return
        }

        if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
            provider.loadItem(forTypeIdentifier: UTType.url.identifier, options: nil) { item, _ in
                if let url = item as? URL {
                    if url.isFileURL {
                        self.copyFile(from: url, into: itemDir, syncQueue: syncQueue)
                    } else {
                        syncQueue.sync { self.extractedTexts.append(url.absoluteString) }
                    }
                }
                done()
            }
            return
        }

        if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
            provider.loadItem(forTypeIdentifier: UTType.plainText.identifier, options: nil) { item, _ in
                if let text = item as? String, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    syncQueue.sync { self.extractedTexts.append(text) }
                }
                done()
            }
            return
        }

        // Generic file fallback.
        provider.loadFileRepresentation(forTypeIdentifier: UTType.data.identifier) { url, _ in
            if let url {
                self.copyFile(from: url, into: itemDir, syncQueue: syncQueue)
            }
            done()
        }
    }

    private func copyFile(from srcURL: URL, into itemDir: URL, syncQueue: DispatchQueue) {
        let fm = FileManager.default
        let base = srcURL.lastPathComponent.isEmpty ? "shared-item" : srcURL.lastPathComponent
        var targetName = base
        var dstURL = itemDir.appendingPathComponent(targetName)
        var suffix = 1
        while fm.fileExists(atPath: dstURL.path) {
            let stem = (base as NSString).deletingPathExtension
            let ext = (base as NSString).pathExtension
            targetName = ext.isEmpty ? "\(stem)-\(suffix)" : "\(stem)-\(suffix).\(ext)"
            dstURL = itemDir.appendingPathComponent(targetName)
            suffix += 1
        }
        if (try? fm.copyItem(at: srcURL, to: dstURL)) != nil {
            syncQueue.sync {
                self.stagedFiles.append(targetName)
            }
        }
    }

    private func writeManifest() -> Bool {
        guard let itemDir = stagedDir else { return false }
        let manifest: [String: Any] = [
            "id": itemDir.lastPathComponent,
            "text": noteField.text.trimmingCharacters(in: .whitespacesAndNewlines),
            "files": stagedFiles,
            "created_at": Int64(Date().timeIntervalSince1970 * 1000)
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: manifest, options: [.prettyPrinted]) else {
            return false
        }
        let manifestURL = itemDir.appendingPathComponent("manifest.json")
        return (try? data.write(to: manifestURL, options: .atomic)) != nil
    }

    @objc private func onSendNow() {
        guard stagingComplete, writeManifest() else { return }
        openMainApp(url: Self.flushURL)
        extensionContext?.completeRequest(returningItems: nil, completionHandler: nil)
    }

    @objc private func onQueue() {
        guard stagingComplete, writeManifest() else { return }
        extensionContext?.completeRequest(returningItems: nil, completionHandler: nil)
    }

    @objc private func onCancel() {
        if let dir = stagedDir {
            try? FileManager.default.removeItem(at: dir)
        }
        let err = NSError(domain: "dev.tailchat.share", code: NSUserCancelledError)
        extensionContext?.cancelRequest(withError: err)
    }

    private func openMainApp(url: URL) {
        var responder: UIResponder? = self
        while let current = responder {
            if let app = current as? UIApplication {
                app.open(url, options: [:], completionHandler: nil)
                return
            }
            responder = current.next
        }
    }
}
