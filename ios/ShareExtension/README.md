# TailChat iOS Share Extension

This directory contains the pure-Swift iOS Share Extension (`ShareExtension.appex`) for TailChat.

Because iOS enforces a strict ~120 MB memory limit on Share Extensions, this target intentionally does **not** link the Go runtime or Tailcat mesh stack. Instead, it writes shared text, links, images, and files into the shared App Group container (`group.dev.tailchat.shared/outbox/<uuid>/manifest.json`) and either:

- **Send Now**: opens `tailchat://flush-outbox` so the main TailChat app immediately ingests and broadcasts the queued items.
- **Queue for Next Open**: leaves the staged bundle in `outbox/` so `plugins/mobilebridge` drains it automatically the next time TailChat becomes active.

## Build Steps in Xcode

1. Create or open a minimal iOS Share Extension target in Xcode:
   - Bundle Identifier: `dev.tailchat.app.ShareExtension`
   - Deployment Target: `iOS 17.0`
   - Replace the generated `ShareViewController.swift` and `Info.plist` with the files in this directory.
2. Under **Signing & Capabilities**, add **App Groups** and enable:
   - `group.dev.tailchat.shared` (must match the main app's App Group in `plugins/mobilebridge/mygo-plugin.json`).
3. Build the extension for `generic/platform=iOS` (or `iOS Simulator` for simulator testing):
   ```sh
   xcodebuild \
     -scheme ShareExtension \
     -configuration Release \
     -destination 'generic/platform=iOS' \
     -derivedDataPath ios/ShareExtension/build/DerivedData \
     build
   ```
4. Copy the resulting `ShareExtension.appex` bundle to:
   ```text
   ios/ShareExtension/build/ShareExtension.appex
   ```
5. Uncomment the `ios.extensions` entry in `mygo.config.ts`:
   ```ts
   ios: {
     minimumSystemVersion: "17.0",
     // extensions: ["ios/ShareExtension/build/ShareExtension.appex"],
   }
   ```
6. Build the iOS app with MyGo (`mygo build -platform ios`).
