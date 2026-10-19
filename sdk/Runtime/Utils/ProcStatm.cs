using System;
using System.IO;

namespace OzxApm.Utils
{
    /// <summary>
    /// The process's resident memory (RSS) from /proc/self/statm (Linux,
    /// Android; APM-9). The file is opened once and re-read from offset 0 (procfs
    /// regenerates it at every read) into a reused buffer: a reading is one
    /// lseek and one read system call, no allocation.
    ///
    /// /proc/self/statm is "size resident shared text lib data dt", in pages;
    /// resident * page size is the RSS. (Android's Debug.getPss would be the
    /// proportional set size, but it walks the page tables (milliseconds) and
    /// needs a JNI call: not affordable at 1 Hz.) Not thread-safe: one reader
    /// thread.
    /// </summary>
    internal sealed class ProcStatm : IDisposable
    {
        internal const string DefaultPath = "/proc/self/statm";

        private readonly string _path;
        private readonly long _pageSize;
        private readonly byte[] _buf = new byte[128];
        private FileStream _stream;
        private int _failures;

        internal ProcStatm(string path = DefaultPath, long pageSize = 0)
        {
            _path = path;
            _pageSize = pageSize > 0 ? pageSize : SystemPageSize();
        }

        private static long SystemPageSize()
        {
            try
            {
                int p = Environment.SystemPageSize;
                return p > 0 ? p : 4096;
            }
            catch
            {
                return 4096; // 4 KB pages; 16 KB pages exist on newer Android devices and are read above
            }
        }

        /// <summary>Gave up after repeated failures (no /proc, permissions).</summary>
        internal bool Disabled => _failures >= 3;

        /// <summary>
        /// The file is opened for every reading (a re-read from offset 0 of the
        /// open file gave nothing usable): one FileStream per reading.
        /// </summary>
        internal bool ReopensEachTime => _reopen;

        private bool _reopen;

        /// <summary>The RSS in bytes, or -1 when it cannot be read.</summary>
        internal long ReadRssBytes()
        {
            if (Disabled)
                return -1;
            try
            {
                bool reread = _stream != null;
                long pages = ReadOnce();
                if (pages < 0 && reread)
                {
                    // Re-reading the open file did not work here: open it anew,
                    // and keep doing so if that works.
                    Close();
                    pages = ReadOnce();
                    if (pages >= 0)
                        _reopen = true;
                }
                if (_reopen)
                    Close();
                if (pages < 0)
                {
                    _failures++;
                    Close();
                    return -1;
                }
                _failures = 0;
                return pages * _pageSize;
            }
            catch
            {
                _failures++;
                Close();
                return -1;
            }
        }

        private long ReadOnce()
        {
            if (_stream == null)
                _stream = new FileStream(_path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite, 1, FileOptions.None);
            else
                _stream.Seek(0, SeekOrigin.Begin);
            int n = 0;
            while (n < _buf.Length)
            {
                int r = _stream.Read(_buf, n, _buf.Length - n);
                if (r <= 0)
                    break;
                n += r;
            }
            return ParseResidentPages(_buf, n);
        }

        /// <summary>The second number of a statm line ("size resident ..."), or -1.</summary>
        internal static long ParseResidentPages(byte[] b, int length)
        {
            int i = 0;
            for (int field = 0; field < 2; field++)
            {
                while (i < length && b[i] == (byte)' ')
                    i++;
                if (i >= length || b[i] < (byte)'0' || b[i] > (byte)'9')
                    return -1;
                long v = 0;
                while (i < length && b[i] >= (byte)'0' && b[i] <= (byte)'9')
                {
                    v = v * 10 + (b[i] - (byte)'0');
                    if (v > (1L << 40))
                        return -1;
                    i++;
                }
                if (field == 1)
                    return v;
            }
            return -1;
        }

        private void Close()
        {
            try
            {
                _stream?.Dispose();
            }
            catch
            {
                // Closing never throws.
            }
            _stream = null;
        }

        public void Dispose() => Close();
    }
}
