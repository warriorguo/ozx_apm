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

        /// <summary>
        /// Unity's own tombstone (persistentDataPath/tombstone_NN), as Unity
        /// 2022.3.62f3 wrote it on the A33 (Android 16) for ForceCrash
        /// AccessViolation (device run 21:40:51): the text of its "E CRASH"
        /// log lines, which are the file's content (the file itself is mode
        /// 600 on the device). The memory map after the backtrace is made up:
        /// the real file has one (about 390 KB in all) and it must not be read.
        /// </summary>
        internal static byte[] UnityTombstone(int pid = 32664, string timestamp = "2026-10-19 21:40:44.166997256+0800", int padTo = 0)
        {
            var sb = new StringBuilder(
                "*** *** *** *** *** *** *** *** *** *** *** *** *** *** *** ***\n" +
                "Version '2022.3.62f3 (96770f904ca7)', Build type 'Release', Scripting Backend 'il2cpp', CPU 'arm64-v8a'\n" +
                "Build fingerprint: 'samsung/a33xnsdxx/a33x:16/BP2A.250605.031.A3/A336EDXUKGZI3:user/release-keys'\n" +
                "Revision: '4'\n" +
                "ABI: 'arm64'\n" +
                "Timestamp: " + timestamp + "\n" +
                "pid: " + pid + ", tid: 32679, name: UnityMain  >>> com.ozx.apmtest <<<\n" +
                "uid: 10318\n" +
                "signal 11 (SIGSEGV), code -6 (SI_TKILL), fault addr --------\n" +
                "    x0  0000000000000000  x1  0000000000007fa7  x2  000000000000000b  x3  6873617243656372\n" +
                "    x4  6873617243656372  x5  6873617243656372  x6  6873617243656372  x7  b4000071e2c25239\n" +
                "    x8  0000000000000083  x9  6fb8365564c36dc1  x10 000000708603f120  x11 000000708603f160\n" +
                "    x12 000000708603f1b0  x13 000000007fffffff  x14 00000000045f6bf2  x15 000000b3cc99bdd0\n" +
                "    x16 00000073bd5e4740  x17 00000073bd5cd140  x18 00000070858e4000  x19 00000073c987d900\n" +
                "    x20 00000073c97bb240  x21 00000073c987d900  x22 0000006fc1050208  x23 000000708603f290\n" +
                "    x24 0000007086040740  x25 000000004a0108d0  x26 000000708603fdb4  x27 000000708603fdb0\n" +
                "    x28 000000708603fdc0  x29 000000708603f5c0\n" +
                "    lr  00000070880fe7c4  sp  000000708603f240  pc  00000073bd5cd148  pst 0000000000001000\n" +
                "backtrace:\n" +
                "      #00 pc 00000000000d0148  /apex/com.android.runtime/lib64/bionic/libc.so (tgkill+8) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)\n" +
                "      #01 pc 000000000023f7c0  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #02 pc 000000000056a030  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n" +
                "      #03 pc 000000000056542c  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n" +
                "      #04 pc 00000000004a41d8  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n" +
                "      #05 pc 00000000004a4124  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n" +
                "      #06 pc 00000000003cd1e8  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #07 pc 00000000003db160  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #08 pc 00000000003e3314  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #09 pc 000000000028ce38  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #10 pc 000000000031e3ec  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #11 pc 000000000031e42c  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #12 pc 000000000031e6d8  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #13 pc 0000000000459178  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #14 pc 000000000046d9f4  /data/app/~~yJz_QaQZdb2msioSeq3MnA==/com.ozx.apmtest-ttbna5sbZigy-UBaXpVgXQ==/lib/arm64/libunity.so (BuildId: a5f31388f37437dc)\n" +
                "      #15 pc 00000000002d1160  /system/framework/arm64/boot.oat (BuildId: 29e0526702d2b52405ceed8866d8685ba6dc2e28)\n" +
                "\n" +
                "memory map (3 entries):\n" +
                "    00000058'8d75e000-00000058'8d762fff r--         0      5000  /system/bin/app_process64 (BuildId: 0123)\n" +
                "    00000070'85800000-00000070'86040fff rw-         0    841000  [anon:stack_and_tls:32679]\n" +
                "      #99 pc 0000000000001234  /data/local/tmp/not-a-frame.so\n");
            while (sb.Length < padTo)
                sb.Append("    00000070'85800000-00000070'86040fff rw-         0    841000  [anon:padding]\n");
            return Encoding.UTF8.GetBytes(sb.ToString());
        }

        [Test]
        public void UnityTombstone_HeaderAndBacktraceOnly()
        {
            var data = UnityTombstone();
            var r = CrashTraceParser.ParseUnityTombstone(data, data.Length);
            Assert.IsNotNull(r);
            Assert.AreEqual(CrashTraceParser.UnityFormat, r.Format);
            Assert.AreEqual(32664, r.Pid);
            Assert.AreEqual(new DateTimeOffset(2026, 10, 19, 13, 40, 44, 166, TimeSpan.Zero).ToUnixTimeMilliseconds(), r.TimestampMs);
            Assert.AreEqual("SIGSEGV", r.Signal);
            Assert.AreEqual("SI_TKILL", r.SignalCode);
            Assert.AreEqual(1, r.Threads.Count);
            Assert.AreEqual("UnityMain", r.Threads[0].Name);
            Assert.AreEqual(16, r.Threads[0].Frames.Count, "the 16 frames of the backtrace; nothing after it");
            StringAssert.StartsWith(
                "Thread \"UnityMain\" tid=32679 crashed\n" +
                "#00 pc 00000000000d0148 /apex/com.android.runtime/lib64/bionic/libc.so (tgkill+8) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)\n" +
                "#01 pc 000000000023f7c0 libunity.so (BuildId: a5f31388f37437dc)\n" +
                "#02 pc 000000000056a030 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n" +
                "#03 pc 000000000056542c libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)\n", r.Stack);
            StringAssert.EndsWith("#15 pc 00000000002d1160 /system/framework/arm64/boot.oat (BuildId: 29e0526702d2b52405ceed8866d8685ba6dc2e28)\n", r.Stack);
            // Registers, the memory map and the install path never leave the device.
            StringAssert.DoesNotContain("6873617243656372", r.Stack);
            StringAssert.DoesNotContain("app_process64", r.Stack);
            StringAssert.DoesNotContain("not-a-frame", r.Stack);
            StringAssert.DoesNotContain("~~yJz_", r.Stack);
            StringAssert.DoesNotContain("samsung", r.Stack);
        }

        [Test]
        public void UnityTombstone_NotAUnityCrashFile_IsNull()
        {
            foreach (var text in new[] { "", "hello", "pid: 12, tid: 13, name: x\nbacktrace:\n", "*** ***\nsignal 11 (SIGSEGV)\nbacktrace:\n#00 pc 0000000000001000  libx.so\n" })
            {
                var b = Encoding.UTF8.GetBytes(text);
                Assert.IsNull(CrashTraceParser.ParseUnityTombstone(b, b.Length), text);
            }
            Assert.IsNull(CrashTraceParser.ParseUnityTombstone(null, 0));
        }

        [Test]
        public void ParseTimestamp_DebuggerdFormat()
        {
            long utc = new DateTimeOffset(2026, 10, 19, 13, 40, 44, 166, TimeSpan.Zero).ToUnixTimeMilliseconds();
            Assert.AreEqual(utc, CrashTraceParser.ParseTimestamp("2026-10-19 21:40:44.166997256+0800"));
            Assert.AreEqual(utc, CrashTraceParser.ParseTimestamp("2026-10-19 08:10:44.166-05:30"));
            Assert.AreEqual(utc - 166, CrashTraceParser.ParseTimestamp("2026-10-19 13:40:44"));
            Assert.AreEqual(utc - 106, CrashTraceParser.ParseTimestamp("2026-10-19 13:40:44.06Z"));
            foreach (var bad in new[] { null, "", "2026-10-19", "yesterday at noon", "2026-13-40 99:99:99.1+0800", "2026-10-19 21:40:44.1+08", "2026-10-19 21:40:44+9900" })
                Assert.AreEqual(0, CrashTraceParser.ParseTimestamp(bad), bad ?? "null");
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
