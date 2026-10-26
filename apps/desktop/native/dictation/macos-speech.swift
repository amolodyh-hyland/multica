import AVFoundation
import Foundation
import Speech

func emit(_ message: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: message) else { exit(1) }
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data([10]))
}

final class Dictation {
    private let recognizer: SFSpeechRecognizer?
    private let audio = AVAudioEngine()
    private let request = SFSpeechAudioBufferRecognitionRequest()
    private var task: SFSpeechRecognitionTask?
    private var tapped = false
    private var finished = false
    private var stopping = false
    private var captureStarted = false
    private var captureStopped = false

    init(locale: String) {
        recognizer = SFSpeechRecognizer(locale: Locale(identifier: locale))
    }

    func check() -> String? {
        guard let recognizer = recognizer,
              recognizer.supportsOnDeviceRecognition else { return "unavailable" }
        let speech = SFSpeechRecognizer.authorizationStatus()
        let microphone = AVCaptureDevice.authorizationStatus(for: .audio)
        if speech == .denied || speech == .restricted || microphone == .denied || microphone == .restricted {
            return "permission_denied"
        }
        return nil
    }

    func start() {
        if let error = check() { finish(error: error); return }
        SFSpeechRecognizer.requestAuthorization { status in
            guard status == .authorized else {
                DispatchQueue.main.async { self.finish(error: "permission_denied") }
                return
            }
            AVCaptureDevice.requestAccess(for: .audio) { allowed in
                DispatchQueue.main.async {
                    if allowed { self.record() }
                    else { self.finish(error: "permission_denied") }
                }
            }
        }
    }

    private func record() {
        guard !finished, let recognizer = recognizer,
              recognizer.supportsOnDeviceRecognition, recognizer.isAvailable else {
            finish(error: "unavailable"); return
        }
        request.requiresOnDeviceRecognition = true
        request.shouldReportPartialResults = false
        let input = audio.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0 && format.channelCount > 0 else { finish(error: "failed"); return }
        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            self.request.append(buffer)
        }
        tapped = true
        task = recognizer.recognitionTask(with: request) { result, error in
            DispatchQueue.main.async {
                if let result = result, result.isFinal {
                    self.finish(text: result.bestTranscription.formattedString)
                } else if let error = error as NSError? {
                    self.finish(error: error.code == 1110 ? "no_speech" : "failed")
                }
            }
        }
        do {
            audio.prepare()
            try audio.start()
            captureStarted = true
            emit(["type": "recording"])
        } catch {
            finish(error: "failed")
        }
    }

    func stop() {
        guard !finished, !stopping else { return }
        stopping = true
        releaseMicrophone()
        request.endAudio()
        task?.finish()
    }

    private func releaseMicrophone() {
        audio.stop()
        if tapped { audio.inputNode.removeTap(onBus: 0); tapped = false }
        if captureStarted && !captureStopped {
            captureStopped = true
            emit(["type": "capture_stopped"])
        }
    }

    func finish(error: String? = nil, text: String = "") {
        guard !finished else { return }
        finished = true
        releaseMicrophone()
        task?.cancel()
        if let error = error { emit(["type": "error", "code": error]) }
        else { emit(["type": "result", "text": text]) }
        exit(0)
    }
}

let dictation = Dictation(locale: CommandLine.arguments.dropFirst().first ?? "en-US")
if CommandLine.arguments.contains("--check") {
    if let error = dictation.check() { emit(["type": "error", "code": error]) }
    else { emit(["type": "available"]) }
    exit(0)
}
DispatchQueue.global(qos: .userInitiated).async {
    while let line = readLine() {
        if line == "stop" { DispatchQueue.main.async { dictation.stop() } }
    }
    DispatchQueue.main.async { dictation.finish(error: "failed") }
}
dictation.start()
RunLoop.main.run()
