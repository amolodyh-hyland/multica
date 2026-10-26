param([string]$Locale = "en-US", [switch]$Check)
$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
try {
    Add-Type -AssemblyName System.Speech
    Add-Type -AssemblyName System.Web.Extensions
    $captureSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'windows-pcm-capture.cs') -Raw
    $speechSource = @'
using System;
using System.Globalization;
using System.IO;
using MulticaDictationNative;
using System.Linq;
using System.Speech.Recognition;
using System.Speech.AudioFormat;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Web.Script.Serialization;

public static class MulticaDictation {
    private static readonly object Gate = new object();
    private static readonly JavaScriptSerializer Json = new JavaScriptSerializer();
    private static readonly StringBuilder Text = new StringBuilder();
    private static readonly ManualResetEvent Done = new ManualResetEvent(false);

    private static void Emit(object message) {
        lock (Gate) {
            Console.WriteLine(Json.Serialize(message));
            Console.Out.Flush();
        }
    }

    public static void Run(string locale, bool check) {
        Run(locale, check, null);
    }

    public static void Run(string locale, bool check, DictationPcmCapture input) {
        SpeechRecognitionEngine engine = null;
        DictationPcmCapture capture = null;
        bool initialized = false;
        try {
            var culture = CultureInfo.GetCultureInfo(locale);
            var installed = SpeechRecognitionEngine.InstalledRecognizers()
                .Where(r => Regex.IsMatch(r.Id, @"^MS-\d+-80-DESK$"));
            var info = installed.FirstOrDefault(r => r.Culture.Name == culture.Name)
                ?? installed.FirstOrDefault(r => r.Culture.TwoLetterISOLanguageName == culture.TwoLetterISOLanguageName);
            if (info == null) { Emit(new { type = "error", code = "unavailable" }); return; }
            engine = new SpeechRecognitionEngine(info);
            engine.LoadGrammar(new DictationGrammar());
            initialized = true;
            if (check) { Emit(new { type = "available" }); return; }
            capture = input ?? new DictationPcmCapture();
            capture.Start();
            Emit(new { type = "recording" });
            string command;
            do { command = Console.ReadLine(); } while (command != null && command != "stop");
            byte[] pcm = capture.Stop();
            Emit(new { type = "capture_stopped" });
            if (command == null) return;
            if (pcm.Length == 0) { Emit(new { type = "result", text = "" }); return; }
            engine.SetInputToAudioStream(new MemoryStream(pcm, false), new SpeechAudioFormatInfo(16000, AudioBitsPerSample.Sixteen, AudioChannel.Mono));
            engine.SpeechRecognized += (sender, e) => {
                lock (Gate) {
                    if (Text.Length > 0) Text.Append(" ");
                    Text.Append(e.Result.Text);
                }
            };
            engine.RecognizeCompleted += (sender, e) => {
                if (e.Error != null) Emit(new { type = "error", code = "failed" });
                else Emit(new { type = "result", text = Text.ToString() });
                Done.Set();
            };
            engine.RecognizeAsync(RecognizeMode.Multiple);
            if (!Done.WaitOne(15000)) {
                engine.RecognizeAsyncCancel();
                Emit(new { type = "error", code = "timeout" });
            }
        } catch (UnauthorizedAccessException) {
            Emit(new { type = "error", code = "permission_denied" });
        } catch (System.Runtime.InteropServices.COMException e) {
            Emit(new { type = "error", code = e.ErrorCode == unchecked((int)0x80070005) ? "permission_denied" : initialized ? "failed" : "unavailable" });
        } catch {
            Emit(new { type = "error", code = initialized ? "failed" : "unavailable" });
        } finally {
            if (capture != null) capture.Dispose();
            if (engine != null) engine.Dispose();
        }
    }
}
'@
    Add-Type -ReferencedAssemblies System.Speech,System.Web.Extensions,System.Core -TypeDefinition ($speechSource + [Environment]::NewLine + $captureSource)
    [MulticaDictation]::Run($Locale, $Check.IsPresent)
} catch {
    [Console]::WriteLine('{"type":"error","code":"unavailable"}')
}
