namespace MulticaDictationNative {
    using System;
    using System.Collections.Generic;
    using System.IO;
    using System.Runtime.InteropServices;
    using System.Threading;

    public sealed class DictationPcmBuffer {
        private readonly object gate = new object();
        private readonly MemoryStream audio = new MemoryStream();
        private byte[] sealedAudio;
        private const int MaxBytes = 16000 * 2 * 120;

        public bool Append(byte[] samples) {
            lock (gate) {
                if (sealedAudio != null) return false;
                int count = Math.Min(samples.Length, MaxBytes - (int)audio.Length);
                audio.Write(samples, 0, count);
                return count == samples.Length;
            }
        }

        public byte[] Seal() {
            lock (gate) {
                if (sealedAudio == null) sealedAudio = audio.ToArray();
                return sealedAudio;
            }
        }
    }

    public sealed class DictationPcmCapture : IDisposable {
        [StructLayout(LayoutKind.Sequential, Pack = 2)]
        public struct WaveFormat {
            public ushort Tag, Channels;
            public uint SamplesPerSecond, BytesPerSecond;
            public ushort BlockAlign, BitsPerSample, ExtraSize;
        }

        [StructLayout(LayoutKind.Sequential)]
        public struct WaveHeader {
            public IntPtr Data;
            public uint Length, Recorded;
            public UIntPtr User;
            public uint Flags, Loops;
            public IntPtr Next;
            public UIntPtr Reserved;
        }

        private sealed class WaveBuffer {
            public IntPtr Header, Data;
            public bool Prepared;
        }

        [DllImport("winmm.dll")]
        private static extern uint waveInOpen(out IntPtr handle, uint device, ref WaveFormat format, IntPtr callback, UIntPtr instance, uint flags);
        [DllImport("winmm.dll")]
        private static extern uint waveInPrepareHeader(IntPtr handle, IntPtr header, uint size);
        [DllImport("winmm.dll")]
        private static extern uint waveInUnprepareHeader(IntPtr handle, IntPtr header, uint size);
        [DllImport("winmm.dll")]
        private static extern uint waveInAddBuffer(IntPtr handle, IntPtr header, uint size);
        [DllImport("winmm.dll")]
        private static extern uint waveInStart(IntPtr handle);
        [DllImport("winmm.dll")]
        private static extern uint waveInStop(IntPtr handle);
        [DllImport("winmm.dll")]
        private static extern uint waveInReset(IntPtr handle);
        [DllImport("winmm.dll")]
        private static extern uint waveInClose(IntPtr handle);

        // The device API is replaceable so shutdown and returned buffers can be tested
        // without loading WinMM or opening a microphone.
        public interface IWaveInput {
            uint Open(out IntPtr handle, ref WaveFormat format, IntPtr callback);
            uint Prepare(IntPtr handle, IntPtr header, uint size);
            uint Unprepare(IntPtr handle, IntPtr header, uint size);
            uint AddBuffer(IntPtr handle, IntPtr header, uint size);
            uint Start(IntPtr handle);
            uint Stop(IntPtr handle);
            uint Reset(IntPtr handle);
            uint Close(IntPtr handle);
        }

        private sealed class WinMmInput : IWaveInput {
            public uint Open(out IntPtr handle, ref WaveFormat format, IntPtr callback) {
                return waveInOpen(out handle, uint.MaxValue, ref format, callback, UIntPtr.Zero, 0x50000);
            }
            public uint Prepare(IntPtr handle, IntPtr header, uint size) { return waveInPrepareHeader(handle, header, size); }
            public uint Unprepare(IntPtr handle, IntPtr header, uint size) { return waveInUnprepareHeader(handle, header, size); }
            public uint AddBuffer(IntPtr handle, IntPtr header, uint size) { return waveInAddBuffer(handle, header, size); }
            public uint Start(IntPtr handle) { return waveInStart(handle); }
            public uint Stop(IntPtr handle) { return waveInStop(handle); }
            public uint Reset(IntPtr handle) { return waveInReset(handle); }
            public uint Close(IntPtr handle) { return waveInClose(handle); }
        }

        private readonly IWaveInput input;

        public DictationPcmCapture() : this(new WinMmInput()) { }

        public DictationPcmCapture(IWaveInput input) {
            this.input = input;
        }

        private readonly object gate = new object();
        private readonly AutoResetEvent ready = new AutoResetEvent(false);
        private readonly List<WaveBuffer> buffers = new List<WaveBuffer>();
        private readonly DictationPcmBuffer audio = new DictationPcmBuffer();
        private static readonly uint HeaderSize = (uint)Marshal.SizeOf(typeof(WaveHeader));
        private IntPtr handle;
        private Thread reader;
        private bool running;
        private int nextBuffer;
        private Exception readError;

        private static void Check(uint result) {
            if (result != 0) throw new InvalidOperationException("Microphone capture failed");
        }

        public void Start() {
            var format = new WaveFormat {
                Tag = 1, Channels = 1, SamplesPerSecond = 16000, BytesPerSecond = 32000,
                BlockAlign = 2, BitsPerSample = 16, ExtraSize = 0,
            };
            Check(input.Open(out handle, ref format, ready.SafeWaitHandle.DangerousGetHandle()));
            for (int i = 0; i < 4; i++) {
                var buffer = new WaveBuffer { Data = Marshal.AllocHGlobal(640), Header = Marshal.AllocHGlobal((int)HeaderSize) };
                buffers.Add(buffer);
                Marshal.StructureToPtr(new WaveHeader { Data = buffer.Data, Length = 640 }, buffer.Header, false);
                Check(input.Prepare(handle, buffer.Header, HeaderSize));
                buffer.Prepared = true;
                Check(input.AddBuffer(handle, buffer.Header, HeaderSize));
            }
            running = true;
            reader = new Thread(ReadBuffers) { IsBackground = true };
            reader.Start();
            Check(input.Start(handle));
        }

        private void ReadBuffers() {
            try {
                while (true) {
                    ready.WaitOne();
                    lock (gate) {
                        if (!running) return;
                        ReadCompletedBuffers(true);
                    }
                }
            } catch (Exception error) {
                lock (gate) { readError = error; }
            }
        }

        private void ReadCompletedBuffers(bool requeue) {
            for (int count = 0; count < buffers.Count; count++) {
                var buffer = buffers[nextBuffer];
                var header = (WaveHeader)Marshal.PtrToStructure(buffer.Header, typeof(WaveHeader));
                if ((header.Flags & 1) == 0) break;
                var samples = new byte[Math.Min(header.Recorded, header.Length)];
                Marshal.Copy(header.Data, samples, 0, samples.Length);
                audio.Append(samples);
                header.Recorded = 0;
                Marshal.StructureToPtr(header, buffer.Header, false);
                if (requeue) Check(input.AddBuffer(handle, buffer.Header, HeaderSize));
                nextBuffer = (nextBuffer + 1) % buffers.Count;
            }
        }

        public byte[] Stop() {
            lock (gate) { running = false; }
            if (handle != IntPtr.Zero) {
                Check(input.Stop(handle));
                Check(input.Reset(handle));
                ready.Set();
                if (reader != null) reader.Join();
                ReadCompletedBuffers(false);
                foreach (var buffer in buffers) {
                    if (buffer.Prepared) Check(input.Unprepare(handle, buffer.Header, HeaderSize));
                    Marshal.FreeHGlobal(buffer.Header);
                    Marshal.FreeHGlobal(buffer.Data);
                }
                buffers.Clear();
                Check(input.Close(handle));
                handle = IntPtr.Zero;
            }
            if (readError != null) throw new InvalidOperationException("Microphone capture failed", readError);
            return audio.Seal();
        }

        public void Dispose() {
            if (handle != IntPtr.Zero) Stop();
            ready.Dispose();
        }
    }
}
