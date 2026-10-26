// @vitest-environment node
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

const powershell = process.platform === "win32"
  ? join(process.env.SystemRoot ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
  : "pwsh";
const available = spawnSync(powershell, ["-NoProfile", "-NonInteractive", "-Command", "exit 0"], { timeout: 10_000 }).status === 0;

const directory = dirname(fileURLToPath(import.meta.url));
const verify = (...args) => spawnSync(powershell, ["-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
  join(directory, "verify-windows-pcm-capture.ps1"), ...args], { encoding: "utf8", timeout: 20_000 });

it.skipIf(!available)("runs the production Windows Stop and helper protocol against continuing fake input", () => {
  const result = verify();
  expect(result.stderr).toBe("");
  expect(result.status).toBe(0);
  expect(result.stdout).toContain("Production Windows Stop, release, late buffers and acknowledgement ordering: OK");
  expect(result.stdout).toContain("PCM stop boundary and memory limit: OK");
}, 30_000);

// These negative controls prove the fixture observes the production calls and
// protocol emission, rather than merely enforcing a cutoff inside its fake.
const shutdownError = /CAPTURE_ACK_BOUNDARY/;
it.skipIf(!available).each([
  ["omitted device stop", "windows-pcm-capture.cs", "Check(input.Stop(handle));", "", shutdownError],
  ["omitted device close", "windows-pcm-capture.cs", "Check(input.Close(handle));", "", shutdownError],
  ["early acknowledgement", "windows-speech.ps1",
    'byte[] pcm = capture.Stop();\n            Emit(new { type = "capture_stopped" });',
    'Emit(new { type = "capture_stopped" });\n            byte[] pcm = capture.Stop();', shutdownError],
  ["post-Stop buffer read", "windows-pcm-capture.cs",
    "if (buffer.Prepared) Check(input.Unprepare(handle, buffer.Header, HeaderSize));",
    "if (buffer.Prepared) Check(input.Unprepare(handle, buffer.Header, HeaderSize));\n                    if (buffer == buffers[0]) ReadCompletedBuffers(false);",
    /PCM_INPUT_BOUNDARY/],
])("detects %s in the production helper", (_name, file, before, after, error) => {
  const temporary = mkdtempSync(join(tmpdir(), "multica-dictation-test-"));
  try {
    const source = readFileSync(join(directory, "../resources/dictation", file), "utf8").replace(/\r\n/g, "\n");
    const mutated = source.replace(before, after);
    expect(mutated).not.toBe(source);
    const path = join(temporary, file);
    writeFileSync(path, mutated);
    const result = verify(file.endsWith(".cs") ? "-CapturePath" : "-HelperPath", path);
    expect(result.status).not.toBe(0);
    expect(result.stderr).toMatch(error);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
}, 30_000);
