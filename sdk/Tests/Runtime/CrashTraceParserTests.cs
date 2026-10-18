using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using NUnit.Framework;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    /// <summary>
    /// APM-7: tombstones (protobuf and text) and ANR traces reduced to the
    /// signal, abort message and the stacks of the crashed thread, UnityMain
    /// and main, in the server's normalized form; nothing else of them leaves
    /// the device.
    /// </summary>
    [TestFixture]
    public class CrashTraceParserTests
    {
        /// <summary>A minimal protobuf writer for tombstone.proto messages.</summary>
        private sealed class Pb
        {
            private readonly List<byte> _b = new List<byte>();

            public Pb Varint(int field, ulong v)
            {
                Key(field, 0);
                Raw(v);
                return this;
            }

            public Pb Str(int field, string s) => Bytes(field, Encoding.UTF8.GetBytes(s));

            public Pb Msg(int field, Pb m) => Bytes(field, m.ToArray());

            public Pb Bytes(int field, byte[] data)
            {
                Key(field, 2);
                Raw((ulong)data.Length);
                _b.AddRange(data);
                return this;
            }

            public Pb Fixed64(int field, ulong v)
            {
                Key(field, 1);
                for (int i = 0; i < 8; i++)
                    _b.Add((byte)(v >> (8 * i)));
                return this;
            }

            private void Key(int field, int wire) => Raw((ulong)((field << 3) | wire));

            private void Raw(ulong v)
            {
                while (v >= 0x80)
                {
                    _b.Add((byte)(v | 0x80));
                    v >>= 7;
                }
                _b.Add((byte)v);
            }

            public byte[] ToArray() => _b.ToArray();
        }

        private const string Il2cppPath = "/data/app/~~Ab12cd==/com.ozx.apmtest-Xy99==/lib/arm64/libil2cpp.so";
        private const string BuildId = "0d65a2d2978f75f06db6c72ff3cf4e5a0c6a2077";

        private static Pb Frame(ulong relPc, string file, string function = null, ulong offset = 0, string buildId = null)
        {
            var f = new Pb().Varint(1, relPc).Varint(2, 0x7a00000000 + relPc).Varint(3, 0x7ff0000000);
            if (function != null)
                f.Str(4, function).Varint(5, offset);
            f.Str(6, file).Varint(7, 0);
            if (buildId != null)
                f.Str(8, buildId);
            return f;
        }

        private static Pb ThreadEntry(int tid, string name, params Pb[] frames)
        {
            var t = new Pb().Varint(1, (ulong)tid).Str(2, name)
                .Msg(3, new Pb().Str(1, "x0").Varint(2, 0xdeadbeef)); // registers: dropped
            foreach (var f in frames)
                t.Msg(4, f);
            t.Msg(5, new Pb().Str(1, "x0").Varint(2, 0x1000).Bytes(3, new byte[] { 1, 2, 3 })); // memory dump: dropped
            return new Pb().Varint(1, (ulong)tid).Msg(2, t);
        }

        internal static byte[] NativeTombstone(string abort = null)
        {
            var signal = new Pb().Varint(1, 11).Str(2, "SIGSEGV").Varint(3, 1).Str(4, "SEGV_MAPERR")
                .Varint(5, 0).Varint(8, 1).Varint(9, 8);
            var tomb = new Pb()
                .Varint(1, 1) // arch arm64
                .Str(2, "google/sdk_gphone64_arm64/emu64a:14/UE1A/1:userdebug/dev-keys")
                .Str(3, "0")
                .Str(4, "2026-10-18 10:00:00.123456789+0000")
                .Varint(5, 12345).Varint(6, 12380).Varint(7, 10123)
                .Str(8, "u:r:untrusted_app:s0")
                .Str(9, "com.ozx.apmtest")
                .Varint(20, 34)
                .Msg(10, signal);
            if (abort != null)
                tomb.Str(14, abort);
            tomb.Msg(15, new Pb().Str(1, "null pointer dereference"))
                .Msg(16, ThreadEntry(12399, "RenderThread", Frame(0x1000, "/vendor/lib64/libGLES.so")))
                .Msg(16, ThreadEntry(12380, "UnityMain",
                    Frame(0x5417bc, Il2cppPath, null, 0, BuildId),
                    Frame(0x123456, "/data/app/~~Ab12cd==/com.ozx.apmtest-Xy99==/base.apk!libil2cpp.so", "il2cpp::vm::Runtime::Invoke(MethodInfo const*, void*, void**, Il2CppException**)", 20, BuildId),
                    Frame(0x4e0dc, "/apex/com.android.runtime/lib64/bionic/libc.so", "abort", 164, "2c4f")))
                .Msg(16, ThreadEntry(12345, "main", Frame(0x9c048, "/apex/com.android.runtime/lib64/bionic/libc.so", "__futex_wait_ex", 168)))
                .Msg(17, new Pb().Varint(1, 0x7a00000000).Varint(2, 0x7a00100000).Str(7, Il2cppPath)) // memory map: dropped
                .Msg(18, new Pb().Str(1, "main").Msg(2, new Pb().Str(5, "secret log line from another component"))) // logcat: dropped
                .Msg(19, new Pb().Varint(1, 42).Str(2, "/data/user/0/com.ozx.apmtest/files/save.dat")); // open fd: dropped
            return tomb.ToArray();
        }

        [Test]
        public void ProtoTombstone_KeepsSignalAbortAndThreeThreads()
        {
            var data = NativeTombstone("il2cpp: Unhandled exception\nsecond line");
            var r = CrashTraceParser.ParseTombstone(data, data.Length);
            Assert.IsNotNull(r);
            Assert.AreEqual("proto", r.Format);
            Assert.AreEqual("SIGSEGV", r.Signal);
            Assert.AreEqual("SEGV_MAPERR", r.SignalCode);
            Assert.AreEqual("0x0000000000000008", r.FaultAddr);
            Assert.AreEqual("il2cpp: Unhandled exception\nsecond line", r.AbortMessage);
            Assert.AreEqual("null pointer dereference", r.Cause);
            Assert.AreEqual(12345, r.Pid);
            Assert.AreEqual(12380, r.Tid);
            string expected =
                "Thread \"UnityMain\" tid=12380 crashed\n" +
                "#00 pc 00000000005417bc libil2cpp.so (BuildId: " + BuildId + ")\n" +
                "#01 pc 0000000000123456 libil2cpp.so (il2cpp::vm::Runtime::Invoke(MethodInfo const*, void*, void**, Il2CppException**)+20) (BuildId: " + BuildId + ")\n" +
                "#02 pc 000000000004e0dc /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: 2c4f)\n" +
                "Thread \"main\" tid=12345\n" +
                "#00 pc 000000000009c048 /apex/com.android.runtime/lib64/bionic/libc.so (__futex_wait_ex+168)\n";
            Assert.AreEqual(expected, r.Stack);
            StringAssert.DoesNotContain("secret", r.Stack);
            StringAssert.DoesNotContain("RenderThread", r.Stack);
            StringAssert.DoesNotContain("save.dat", r.Stack);
            StringAssert.DoesNotContain("~~Ab12cd", r.Stack);
        }

        [Test]
        public void TextTombstone_IsParsedToo()
        {
            string text = "*** *** *** *** *** *** *** *** *** *** *** *** *** *** *** ***\n" +
                "Build fingerprint: 'google/x/y:11/RP1A/1:user/release-keys'\n" +
                "pid: 4000, tid: 4021, name: UnityMain  >>> com.ozx.apmtest <<<\n" +
                "uid: 10123\n" +
                "signal 6 (SIGABRT), code -1 (SI_QUEUE), fault addr --------\n" +
                "Abort message: 'Unity: fatal error in il2cpp'\n" +
                "    x0  0000000000000000  x1  0000000000000fb5\n" +
                "\n" +
                "backtrace:\n" +
                "      #00 pc 000000000004e0dc  /apex/com.android.runtime/lib64/bionic/libc.so (abort+164) (BuildId: 2c4f)\n" +
                "      #01 pc 00000000005417bc  " + Il2cppPath + " (BuildId: 0D65A2D2)\n" +
                "\n" +
                "stack:\n" +
                "         0000007fd1d0e0a0  0000000000000000\n" +
                "      #02 pc 0000000000000000  not a frame of the backtrace\n" +
                "--- --- --- --- --- --- --- --- --- --- --- --- --- --- --- ---\n" +
                "pid: 4000, tid: 4000, name: com.ozx.apmtest  >>> com.ozx.apmtest <<<\n" +
                "backtrace:\n" +
                "      #00 pc 000000000009c048  /apex/com.android.runtime/lib64/bionic/libc.so (__futex_wait_ex+168)\n";
            var bytes = Encoding.UTF8.GetBytes(text);
            var r = CrashTraceParser.ParseTombstone(bytes, bytes.Length);
            Assert.IsNotNull(r);
            Assert.AreEqual("text", r.Format);
            Assert.AreEqual("SIGABRT", r.Signal);
            Assert.AreEqual("SI_QUEUE", r.SignalCode);
            Assert.AreEqual("Unity: fatal error in il2cpp", r.AbortMessage);
            Assert.AreEqual(2, r.Threads.Count);
            Assert.AreEqual("UnityMain", r.Threads[0].Name);
            Assert.AreEqual(2, r.Threads[0].Frames.Count, "frames after the backtrace are not part of it");
            Assert.AreEqual("libil2cpp.so", r.Threads[0].Frames[1].Module);
            Assert.AreEqual("0d65a2d2", r.Threads[0].Frames[1].BuildId);
            Assert.AreEqual(4000, r.Threads[1].Tid, "the main thread (tid = pid)");
        }

        [Test]
        public void AnrTrace_KeepsMainAndUnityMainOnly()
        {
            string trace = "\n----- pid 12345 at 2026-10-18 10:00:00.123 -----\n" +
                "Cmd line: com.ozx.apmtest\n" +
                "Build fingerprint: 'google/x/y:14/UE1A/1:user/release-keys'\n" +
                "\n" +
                "\"main\" prio=5 tid=1 Sleeping\n" +
                "  | group=\"main\" sCount=1 ucsCount=0 flags=1 obj=0x72f0 self=0xb400\n" +
                "  | sysTid=12345 nice=-10 cgrp=top-app sched=0/0 handle=0x7d\n" +
                "  native: #00 pc 00000000000a1a6c  /apex/com.android.runtime/lib64/bionic/libc.so (nanosleep+12) (BuildId: 2c4f)\n" +
                "  at java.lang.Thread.sleep(Native method)\n" +
                "  - sleeping on <0x0e7c> (a java.lang.Object)\n" +
                "  at com.unity3d.player.UnityPlayer$3.run(UnityPlayer.java:120)\n" +
                "\n" +
                "\"Signal Catcher\" daemon prio=10 tid=7 Runnable\n" +
                "  native: #00 pc 0000000000001000  /apex/libart.so (art::DumpStack+1)\n" +
                "\n" +
                "\"UnityMain\" prio=5 tid=20 Native\n" +
                "  | sysTid=12380 nice=-4\n" +
                "  native: #00 pc 00000000005417bc  /data/app/~~Ab12cd==/com.ozx.apmtest-Xy99==/split_config.arm64_v8a.apk!libil2cpp.so (offset 0x1234000) (BuildId: " + BuildId + ")\n" +
                "  native: #01 pc 0000000000541470  /data/app/~~Ab12cd==/com.ozx.apmtest-Xy99==/lib/arm64/libil2cpp.so (BuildId: " + BuildId + ")\n" +
                "  (no managed stack frames)\n" +
                "\n----- end 12345 -----\n";
            var r = CrashTraceParser.ParseAnr(new StringReader(trace), 1 << 20);
            Assert.IsNotNull(r);
            Assert.AreEqual("anr", r.Format);
            string expected =
                "Thread \"main\" tid=12345 crashed\n" +
                "#00 pc 00000000000a1a6c /apex/com.android.runtime/lib64/bionic/libc.so (nanosleep+12) (BuildId: 2c4f)\n" +
                "at java.lang.Thread.sleep(Native method)\n" +
                "at com.unity3d.player.UnityPlayer$3.run(UnityPlayer.java:120)\n" +
                "Thread \"UnityMain\" tid=12380\n" +
                "#00 pc 00000000005417bc libil2cpp.so (BuildId: " + BuildId + ")\n" +
                "#01 pc 0000000000541470 libil2cpp.so (BuildId: " + BuildId + ")\n";
            Assert.AreEqual(expected, r.Stack);
            StringAssert.DoesNotContain("Signal Catcher", r.Stack);
        }

        [Test]
        public void HostileInput_NeverThrowsAndIsBounded()
        {
            var rnd = new System.Random(7);
            for (int i = 0; i < 2000; i++)
            {
                var b = new byte[rnd.Next(0, 300)];
                rnd.NextBytes(b);
                Assert.DoesNotThrow(() => CrashTraceParser.ParseTombstone(b, b.Length));
            }
            // A truncated real tombstone: no exception.
            var full = NativeTombstone();
            for (int cut = 0; cut < full.Length; cut += 7)
                Assert.DoesNotThrow(() => CrashTraceParser.ParseTombstone(full, cut));
            // Thousands of frames: at most MaxFramesPerThread per thread, the stack bounded.
            var sb = new StringBuilder("\"main\" prio=5 tid=1 Native\n");
            for (int i = 0; i < 5000; i++)
                sb.Append("  native: #00 pc 0000000000001000  /data/app/x/lib/arm64/libil2cpp.so (A\u0001B+1)\n");
            var r = CrashTraceParser.ParseAnr(new StringReader(sb.ToString()), 1 << 22);
            Assert.AreEqual(CrashTraceParser.MaxFramesPerThread, r.Threads[0].Frames.Count);
            Assert.LessOrEqual(r.Stack.Length, CrashTraceParser.MaxStackChars);
            StringAssert.DoesNotContain("\u0001", r.Stack);
        }

        [Test]
        public void ModuleNames()
        {
            Assert.AreEqual("libil2cpp.so", CrashTraceParser.ModuleName(Il2cppPath));
            Assert.AreEqual("libunity.so", CrashTraceParser.ModuleName("/data/app/x/base.apk!libunity.so"));
            Assert.AreEqual("/system/lib64/libc.so", CrashTraceParser.ModuleName("/system/lib64/libc.so"));
            Assert.AreEqual("[anon:dalvik-main space]", CrashTraceParser.ModuleName("[anon:dalvik-main space]"));
            Assert.AreEqual("libfoo.so", CrashTraceParser.ModuleName("/system/app/X/X.apk!libfoo.so"));
        }
    }
}
