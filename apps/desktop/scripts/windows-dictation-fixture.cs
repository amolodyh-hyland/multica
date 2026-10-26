// Test-only replacements for WinMM, System.Speech and protocol serialization.
// The packaged helper's Run(), capture Start()/Stop(), and PCM stream are real.
using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using MulticaDictationNative;

public static class WindowsDictationRegression {
    private sealed class ContinuingInput : DictationPcmCapture.IWaveInput {
        public readonly List<IntPtr> Headers = new List<IntPtr>();
        public bool Stopped, WasReset, Closed;
        public int LateWrites, DeliveryAttempts;

        private void Write(IntPtr pointer, string text, bool complete) {
            var header = (DictationPcmCapture.WaveHeader)Marshal.PtrToStructure(pointer, typeof(DictationPcmCapture.WaveHeader));
            var bytes = Encoding.UTF8.GetBytes(text);
            Marshal.Copy(bytes, 0, header.Data, bytes.Length);
            header.Recorded = (uint)bytes.Length;
            header.Flags = complete ? 1u : 0u;
            Marshal.StructureToPtr(header, pointer, false);
        }

        public void DeliverLateBuffer() {
            DeliveryAttempts++;
            // Deliberately keep delivering after Stop; no fake "capturing" flag
            // suppresses these bytes. Only production buffer draining/release
            // and sealing can prevent them from reaching recognition.
            if (Headers.Count > 0) {
                Write(Headers[0], "after stop", true);
                LateWrites++;
            }
        }

        public uint Open(out IntPtr handle, ref DictationPcmCapture.WaveFormat format, IntPtr callback) {
            Require(format.SamplesPerSecond == 16000 && format.BitsPerSample == 16 && format.Channels == 1, "Unexpected PCM format");
            handle = new IntPtr(1);
            return 0;
        }
        public uint Prepare(IntPtr handle, IntPtr header, uint size) { Headers.Add(header); return 0; }
        public uint AddBuffer(IntPtr handle, IntPtr header, uint size) { return 0; }
        public uint Start(IntPtr handle) {
            // A partial buffer captured before Stop, returned by the real drain
            // path after the device has been stopped/reset. No reader timing.
            Write(Headers[0], "before stop", false);
            Events.Add("start");
            return 0;
        }
        public uint Stop(IntPtr handle) { Stopped = true; Events.Add("stop"); return 0; }
        public uint Reset(IntPtr handle) {
            WasReset = true;
            Events.Add("reset");
            foreach (var pointer in Headers) {
                var header = (DictationPcmCapture.WaveHeader)Marshal.PtrToStructure(pointer, typeof(DictationPcmCapture.WaveHeader));
                header.Flags = 1;
                Marshal.StructureToPtr(header, pointer, false);
            }
            return 0;
        }
        public uint Unprepare(IntPtr handle, IntPtr header, uint size) {
            // This write occurs inside the production Stop(), after its final
            // drain, while the buffer still exists. It must never be read again.
            DeliverLateBuffer();
            Headers.Remove(header);
            Events.Add("unprepare");
            return 0;
        }
        public uint Close(IntPtr handle) {
            Require(Headers.Count == 0, "Device closed before buffers were released");
            Closed = true;
            Events.Add("close");
            return 0;
        }
    }

    private sealed class ProtocolWriter : StringWriter {
        public override void WriteLine(string value) {
            Events.Add(value);
            if (value == "capture_stopped") {
                // Inspect at the actual Emit(), not after Run's finally block
                // (which could otherwise mask an early acknowledgement).
                if (!Input.Stopped || !Input.WasReset || !Input.Closed || Input.Headers.Count != 0)
                    Failure = "CAPTURE_ACK_BOUNDARY: acknowledgement preceded device stop/reset/release";
                Input.DeliverLateBuffer();
            }
        }
    }

    private static ContinuingInput Input;
    public static readonly List<string> Events = new List<string>();
    public static string RecognitionInput;
    private static string Failure;

    public static void Require(bool condition, string message) {
        if (!condition) throw new InvalidOperationException(message);
    }

    public static void ReceiveRecognitionInput(Stream stream) {
        Events.Add("recognition_input");
        Input.DeliverLateBuffer();
        using (var copy = new MemoryStream()) {
            stream.CopyTo(copy);
            RecognitionInput = Encoding.UTF8.GetString(copy.ToArray());
        }
    }

    public static void Run() {
        Input = new ContinuingInput();
        var previousIn = Console.In;
        var previousOut = Console.Out;
        try {
            Console.SetIn(new StringReader("stop\n"));
            Console.SetOut(new ProtocolWriter());
            using (var capture = new DictationPcmCapture(Input)) {
                MulticaDictation.Run("en-US", false, capture);
            }
        } finally {
            Console.SetIn(previousIn);
            Console.SetOut(previousOut);
        }
        Require(Failure == null, Failure);
        Require(Input.Stopped && Input.WasReset && Input.Closed, "Production Stop did not shut down the device");
        Require(Input.LateWrites == 4 && Input.DeliveryAttempts >= 6, "Fixture did not deliver continuing post-Stop buffers");
        Require(RecognitionInput == "before stop", "PCM_INPUT_BOUNDARY: recognition lost pre-Stop input or included post-Stop input: " + RecognitionInput);
        Require(String.Join(",", Events) == "start,recording,stop,reset,unprepare,unprepare,unprepare,unprepare,close,capture_stopped,recognition_input,transcribe,result",
            "Unexpected shutdown/protocol/recognition ordering: " + String.Join(",", Events));
    }
}

namespace System.Web.Script.Serialization {
    public sealed class JavaScriptSerializer {
        // Keep the real helper Emit() path, retaining protocol message types.
        public string Serialize(object message) {
            return (string)message.GetType().GetProperty("type").GetValue(message, null);
        }
    }
}

namespace System.Speech.AudioFormat {
    public enum AudioBitsPerSample { Sixteen }
    public enum AudioChannel { Mono }
    public sealed class SpeechAudioFormatInfo {
        public SpeechAudioFormatInfo(int rate, AudioBitsPerSample bits, AudioChannel channel) { }
    }
}

namespace System.Speech.Recognition {
    public sealed class RecognizerInfo {
        public string Id = "MS-409-80-DESK";
        public CultureInfo Culture = CultureInfo.GetCultureInfo("en-US");
    }
    public sealed class DictationGrammar { }
    public enum RecognizeMode { Multiple }
    public sealed class RecognitionResult { public string Text; }
    public sealed class SpeechRecognizedEventArgs : EventArgs { public RecognitionResult Result; }
    public sealed class RecognizeCompletedEventArgs : EventArgs { public Exception Error = null; }
    public sealed class SpeechRecognitionEngine : IDisposable {
        public event EventHandler<SpeechRecognizedEventArgs> SpeechRecognized;
        public event EventHandler<RecognizeCompletedEventArgs> RecognizeCompleted;
        public static IEnumerable<RecognizerInfo> InstalledRecognizers() { return new[] { new RecognizerInfo() }; }
        public SpeechRecognitionEngine(RecognizerInfo info) { }
        public void LoadGrammar(DictationGrammar grammar) { }
        public void SetInputToAudioStream(Stream stream, AudioFormat.SpeechAudioFormatInfo format) {
            WindowsDictationRegression.ReceiveRecognitionInput(stream);
        }
        public void RecognizeAsync(RecognizeMode mode) {
            WindowsDictationRegression.Events.Add("transcribe");
            SpeechRecognized(this, new SpeechRecognizedEventArgs {
                Result = new RecognitionResult { Text = WindowsDictationRegression.RecognitionInput }
            });
            RecognizeCompleted(this, new RecognizeCompletedEventArgs());
        }
        public void RecognizeAsyncCancel() { }
        public void Dispose() { }
    }
}
