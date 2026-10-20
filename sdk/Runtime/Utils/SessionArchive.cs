using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Security.Cryptography;
using System.Text;

namespace OzxApm.Utils
{
    /// <summary>
    /// Zips an AiTelemetry session directory (APM-13) the way the server expects
    /// it: the directory itself is the archive's only top-level entry
    /// (<c>&lt;session&gt;/session.json</c>, <c>&lt;session&gt;/room01_x/...</c>), so
    /// unzipping a download gives exactly OZX's <c>SessionFolder/&lt;session&gt;/</c>.
    ///
    /// Streaming: files are read and compressed in 64 KB steps into the output
    /// file, and the SHA-256 of the archive is computed while it is written (no
    /// second pass, nothing held in memory but the caller's buffer, the deflater's
    /// state and the central directory entries). Entries use data descriptors
    /// (no seek back), UTF-8 names, Deflate (Fastest) or Stored when the runtime
    /// has no working DeflateStream. No ZIP64: each file and the archive must
    /// stay below 4 GiB (the server accepts far less). Background thread only.
    /// </summary>
    internal static class SessionArchive
    {
        internal const int BufferSize = 64 * 1024;
        private const uint MaxZipSize = 0xFFFFFFF0u;

        internal struct Result
        {
            public long Size;
            public string Sha256;
            public int FileCount;
            public long RawBytes;
            public bool Deflated;
        }

        internal sealed class FileEntry
        {
            public string FullPath;
            public string Name; // "<session>/<relative path>" with '/'
            public long Length;
            public DateTime Modified;
        }

        /// <summary>Set to make Create use Stored entries (tests; a runtime without zlib).</summary>
        internal static bool ForceStoredForTests;
        private static volatile bool s_deflateBroken;
        /// <summary>DeflateStream failed on this runtime: archives are Stored.</summary>
        internal static bool DeflateBroken => s_deflateBroken;

        /// <summary>
        /// The regular files of the directory, sorted by entry name, with their size
        /// and time (the snapshot the archive is made of). Hidden files ("." prefix)
        /// are skipped.
        /// </summary>
        internal static List<FileEntry> Snapshot(string directory)
        {
            var root = new DirectoryInfo(directory);
            string prefix = root.Name + "/";
            var list = new List<FileEntry>();
            var pending = new Stack<KeyValuePair<DirectoryInfo, string>>();
            pending.Push(new KeyValuePair<DirectoryInfo, string>(root, prefix));
            while (pending.Count > 0)
            {
                var item = pending.Pop();
                foreach (var info in item.Key.GetFileSystemInfos())
                {
                    if (info.Name.StartsWith(".", StringComparison.Ordinal))
                        continue;
                    if ((info.Attributes & FileAttributes.ReparsePoint) != 0)
                        continue; // links: never followed, never archived
                    if (info is DirectoryInfo sub)
                    {
                        pending.Push(new KeyValuePair<DirectoryInfo, string>(sub, item.Value + sub.Name + "/"));
                    }
                    else if (info is FileInfo file)
                    {
                        list.Add(new FileEntry { FullPath = file.FullName, Name = item.Value + file.Name, Length = file.Length, Modified = file.LastWriteTimeUtc });
                    }
                }
            }
            list.Sort((a, b) => string.CompareOrdinal(a.Name, b.Name));
            return list;
        }

        /// <summary>True when a later snapshot shows the same files, sizes and times.</summary>
        internal static bool SameSnapshot(List<FileEntry> a, List<FileEntry> b)
        {
            if (a.Count != b.Count)
                return false;
            for (int i = 0; i < a.Count; i++)
            {
                if (a[i].Name != b[i].Name || a[i].Length != b[i].Length || a[i].Modified != b[i].Modified)
                    return false;
            }
            return true;
        }

        /// <summary>
        /// Writes the archive of <paramref name="files"/> (a Snapshot) to
        /// <paramref name="zipPath"/>. Each file contributes exactly its snapshot
        /// length (a file that grew meanwhile is cut, one that shrank fails: the
        /// caller compares snapshots before and after). <paramref name="cancelled"/>
        /// is polled between steps.
        /// </summary>
        internal static Result Create(List<FileEntry> files, string zipPath, byte[] buffer, Func<bool> cancelled)
        {
            bool deflate = !ForceStoredForTests && !s_deflateBroken;
            try
            {
                return Write(files, zipPath, buffer, cancelled, deflate);
            }
            catch (Exception ex) when (deflate && IsMissingZlib(ex))
            {
                // A runtime without a working DeflateStream: store instead.
                s_deflateBroken = true;
                return Write(files, zipPath, buffer, cancelled, false);
            }
        }

        private static bool IsMissingZlib(Exception ex)
        {
            for (var e = ex; e != null; e = e.InnerException)
            {
                if (e is DllNotFoundException || e is EntryPointNotFoundException || e is NotSupportedException || e is NotImplementedException ||
                    e is TypeLoadException)
                    return true;
            }
            return false;
        }

        private struct CentralEntry
        {
            public byte[] Name;
            public uint Crc;
            public uint Compressed;
            public uint Uncompressed;
            public uint Offset;
            public ushort Time;
            public ushort Date;
            public ushort Method;
        }

        private static Result Write(List<FileEntry> files, string zipPath, byte[] buffer, Func<bool> cancelled, bool deflate)
        {
            var result = new Result { Deflated = deflate };
            var central = new List<CentralEntry>(files.Count);
            using (var sha = SHA256.Create())
            using (var file = new FileStream(zipPath, FileMode.Create, FileAccess.Write, FileShare.None, BufferSize))
            {
                var output = new HashingStream(file, sha);
                foreach (var f in files)
                {
                    if (cancelled != null && cancelled())
                        throw new OperationCanceledException();
                    if (f.Length > MaxZipSize)
                        throw new IOException($"{f.Name} is larger than 4 GiB");
                    var e = new CentralEntry
                    {
                        Name = Encoding.UTF8.GetBytes(f.Name),
                        Offset = CheckedOffset(output.Position),
                        Method = (ushort)(deflate ? 8 : 0)
                    };
                    DosTime(f.Modified, out e.Time, out e.Date);
                    WriteLocalHeader(output, e);
                    long start = output.Position;
                    uint crc = 0xFFFFFFFFu;
                    long remaining = f.Length;
                    using (var input = new FileStream(f.FullPath, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete, BufferSize))
                    {
                        Stream sink = output;
                        DeflateStream deflater = null;
                        if (deflate)
                        {
                            deflater = new DeflateStream(output, CompressionLevel.Fastest, true);
                            sink = deflater;
                        }
                        try
                        {
                            while (remaining > 0)
                            {
                                int n = input.Read(buffer, 0, (int)Math.Min(buffer.Length, remaining));
                                if (n <= 0)
                                    throw new IOException($"{f.Name} shrank while it was archived");
                                crc = Crc32.Update(crc, buffer, 0, n);
                                sink.Write(buffer, 0, n);
                                remaining -= n;
                                if (cancelled != null && cancelled())
                                    throw new OperationCanceledException();
                            }
                        }
                        finally
                        {
                            deflater?.Dispose(); // flushes the final block into output (left open)
                        }
                    }
                    e.Crc = ~crc;
                    e.Compressed = CheckedOffset(output.Position - start);
                    e.Uncompressed = (uint)f.Length;
                    WriteDataDescriptor(output, e);
                    central.Add(e);
                    result.FileCount++;
                    result.RawBytes += f.Length;
                }
                uint cdStart = CheckedOffset(output.Position);
                foreach (var e in central)
                    WriteCentralHeader(output, e);
                uint cdSize = CheckedOffset(output.Position - cdStart);
                WriteEnd(output, central.Count, cdSize, cdStart);
                output.Flush();
                sha.TransformFinalBlock(buffer, 0, 0);
                result.Sha256 = Hex(sha.Hash);
                result.Size = output.Position;
            }
            return result;
        }

        private static uint CheckedOffset(long value)
        {
            if (value < 0 || value > MaxZipSize)
                throw new IOException("the archive would be larger than 4 GiB");
            return (uint)value;
        }

        internal static string Hex(byte[] bytes)
        {
            const string digits = "0123456789abcdef";
            var c = new char[bytes.Length * 2];
            for (int i = 0; i < bytes.Length; i++)
            {
                c[2 * i] = digits[bytes[i] >> 4];
                c[2 * i + 1] = digits[bytes[i] & 15];
            }
            return new string(c);
        }

        private static void DosTime(DateTime utc, out ushort time, out ushort date)
        {
            var t = utc.Year < 1980 ? new DateTime(1980, 1, 1) : utc;
            time = (ushort)((t.Hour << 11) | (t.Minute << 5) | (t.Second / 2));
            date = (ushort)(((t.Year - 1980) << 9) | (t.Month << 5) | t.Day);
        }

        // Version needed 2.0, general purpose flags: bit 3 (data descriptor), bit 11 (UTF-8).
        private const ushort Flags = (1 << 3) | (1 << 11);

        private static void WriteLocalHeader(Stream s, CentralEntry e)
        {
            var h = new byte[30];
            Put32(h, 0, 0x04034b50);
            Put16(h, 4, 20);
            Put16(h, 6, Flags);
            Put16(h, 8, e.Method);
            Put16(h, 10, e.Time);
            Put16(h, 12, e.Date);
            // crc and sizes in the data descriptor
            Put16(h, 26, (ushort)e.Name.Length);
            s.Write(h, 0, h.Length);
            s.Write(e.Name, 0, e.Name.Length);
        }

        private static void WriteDataDescriptor(Stream s, CentralEntry e)
        {
            var d = new byte[16];
            Put32(d, 0, 0x08074b50);
            Put32(d, 4, e.Crc);
            Put32(d, 8, e.Compressed);
            Put32(d, 12, e.Uncompressed);
            s.Write(d, 0, d.Length);
        }

        private static void WriteCentralHeader(Stream s, CentralEntry e)
        {
            var h = new byte[46];
            Put32(h, 0, 0x02014b50);
            Put16(h, 4, 20);   // made by: MS-DOS / FAT, version 2.0 (no Unix modes: plain files)
            Put16(h, 6, 20);
            Put16(h, 8, Flags);
            Put16(h, 10, e.Method);
            Put16(h, 12, e.Time);
            Put16(h, 14, e.Date);
            Put32(h, 16, e.Crc);
            Put32(h, 20, e.Compressed);
            Put32(h, 24, e.Uncompressed);
            Put16(h, 28, (ushort)e.Name.Length);
            Put32(h, 42, e.Offset);
            s.Write(h, 0, h.Length);
            s.Write(e.Name, 0, e.Name.Length);
        }

        private static void WriteEnd(Stream s, int count, uint cdSize, uint cdStart)
        {
            if (count > 0xFFFF)
                throw new IOException("more than 65535 files");
            var h = new byte[22];
            Put32(h, 0, 0x06054b50);
            Put16(h, 8, (ushort)count);
            Put16(h, 10, (ushort)count);
            Put32(h, 12, cdSize);
            Put32(h, 16, cdStart);
            s.Write(h, 0, h.Length);
        }

        private static void Put16(byte[] b, int i, ushort v)
        {
            b[i] = (byte)v;
            b[i + 1] = (byte)(v >> 8);
        }

        private static void Put32(byte[] b, int i, uint v)
        {
            b[i] = (byte)v;
            b[i + 1] = (byte)(v >> 8);
            b[i + 2] = (byte)(v >> 16);
            b[i + 3] = (byte)(v >> 24);
        }

        /// <summary>Writes through to the file, hashing every byte, counting the position.</summary>
        private sealed class HashingStream : Stream
        {
            private readonly Stream _inner;
            private readonly HashAlgorithm _hash;
            private long _position;

            public HashingStream(Stream inner, HashAlgorithm hash)
            {
                _inner = inner;
                _hash = hash;
            }

            public override bool CanRead => false;
            public override bool CanSeek => false;
            public override bool CanWrite => true;
            public override long Length => _position;
            public override long Position
            {
                get => _position;
                set => throw new NotSupportedException();
            }

            public override void Write(byte[] buffer, int offset, int count)
            {
                if (count <= 0)
                    return;
                _hash.TransformBlock(buffer, offset, count, null, 0);
                _inner.Write(buffer, offset, count);
                _position += count;
            }

            public override void WriteByte(byte value)
            {
                Write(new[] { value }, 0, 1);
            }

            public override void Flush() => _inner.Flush();
            public override int Read(byte[] buffer, int offset, int count) => throw new NotSupportedException();
            public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
            public override void SetLength(long value) => throw new NotSupportedException();
        }
    }

    /// <summary>CRC-32 (IEEE 802.3, as zip uses), table-driven.</summary>
    internal static class Crc32
    {
        private static readonly uint[] Table = MakeTable();

        private static uint[] MakeTable()
        {
            var t = new uint[256];
            for (uint i = 0; i < 256; i++)
            {
                uint c = i;
                for (int k = 0; k < 8; k++)
                    c = (c & 1) != 0 ? 0xEDB88320u ^ (c >> 1) : c >> 1;
                t[i] = c;
            }
            return t;
        }

        /// <summary>Continues a CRC (start with 0xFFFFFFFF, finish with ~).</summary>
        public static uint Update(uint crc, byte[] data, int offset, int count)
        {
            var t = Table;
            for (int i = offset; i < offset + count; i++)
                crc = t[(crc ^ data[i]) & 0xFF] ^ (crc >> 8);
            return crc;
        }
    }
}
