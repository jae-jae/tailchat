import { defineConfig } from "mygo-cli";

export default defineConfig({
  name: "TailChat",
  slug: "tailchat",
  identifier: "dev.tailchat.app",
  version: "0.1.0",
  icon: "resources/icon.png",
  main: "./cmd/tailchat",
  out: "dist",
  urlSchemes: ["tailchat"],

  macos: {
    icon: "resources/macos/AppIcon.icns",
    minimumSystemVersion: "13.0",
  },

  windows: {
    displayName: "TailChat",
  },

  linux: {
    comment: "Private P2P device chat and file sync powered by Tailcat",
    categories: ["Network", "Chat", "Utility"],
    webview: false,
    command: "tailchat",
  },

  ios: {
    minimumSystemVersion: "17.0",
    backgroundModes: ["fetch", "processing"],
    // After building ios/ShareExtension/build/ShareExtension.appex in Xcode
    // (see ios/ShareExtension/README.md), uncomment to embed it into the .ipa:
    // extensions: ["ios/ShareExtension/build/ShareExtension.appex"],
  },

  android: {
    minSdk: 26,
    permissions: [
      "FOREGROUND_SERVICE",
      "FOREGROUND_SERVICE_DATA_SYNC",
      "POST_NOTIFICATIONS",
    ],
  },
});
