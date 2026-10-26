import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");

export function bundleDictation(platform = process.platform) {
  if (platform !== "darwin") return;
  if (process.platform !== "darwin") throw new Error("macOS dictation must be built on macOS");
  const output = resolve(root, "resources/dictation/multica-dictation");
  mkdirSync(dirname(output), { recursive: true });
  const slices = ["arm64", "x86_64"].map((arch) => {
    const slice = `${output}-${arch}`;
    execFileSync("xcrun", ["swiftc", "-O", "-swift-version", "5",
      "-target", `${arch}-apple-macos12.0`,
      "-module-cache-path", resolve(root, "out/swift-module-cache"),
      "-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist",
      "-Xlinker", resolve(root, "native/dictation/Info.plist"),
      resolve(root, "native/dictation/macos-speech.swift"), "-o", slice], { stdio: "inherit" });
    return slice;
  });
  execFileSync("xcrun", ["lipo", "-create", ...slices, "-output", output], { stdio: "inherit" });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) bundleDictation();
