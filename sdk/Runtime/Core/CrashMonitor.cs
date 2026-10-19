using System;
using System.IO;
using System.Threading;
using OzxApm.Collectors;
using OzxApm.Models;

namespace OzxApm.Core
{
    /// <summary>
    /// Ties the crash journal of this process and the report of earlier
    /// processes (APM-7) to the SDK's life cycle and consent (APM-14):
    ///
    /// - Technical consent Granted: the journal is written; the first time in
    ///   the process, earlier processes are reported (on a background thread).
    /// - Not Granted: the journal stops and this process's journal is deleted
    ///   (nothing is persisted without consent); journals of earlier processes
    ///   are kept while Undecided and deleted when Denied, with the rest of the
    ///   directory.
    /// - ResetDeviceId: journals of earlier processes (they carry the old id)
    ///   are deleted; the journal is rewritten with the new id.
    /// - Clean shutdown: this process's journal is deleted (not a crash).
    ///
    /// Main thread except where noted.
    /// </summary>
    internal sealed class CrashMonitor
    {
        private readonly string _dir;
        private readonly JournalInputs _inputs;
        private readonly IExitInfoSource _source;
        private readonly Action<CrashEvent> _enqueue;
        private readonly Func<bool> _allowed;
        private readonly Func<int, bool> _waitPersisted;
        private readonly bool _useThreads;
        private CrashJournal _journal;
        private CrashReporter _reporter;
        private Thread _reportThread;
        private bool _reportStarted;

        internal CrashMonitor(string dir, JournalInputs inputs, IExitInfoSource source, Action<CrashEvent> enqueue,
            Func<bool> allowed, Func<int, bool> waitPersisted, bool useThreads)
        {
            _dir = dir;
            _inputs = inputs;
            _source = source;
            _enqueue = enqueue;
            _allowed = allowed;
            _waitPersisted = waitPersisted;
            _useThreads = useThreads;
        }

        internal string Directory => _dir;
        internal CrashJournal Journal => _journal;
        internal CrashReporter Reporter => _reporter;
        internal JournalInputs Inputs => _inputs;

        internal void SetPerf(PerformanceCollector perf) => _inputs.Perf = perf;

        /// <summary>Collecting (technical consent Granted) started or stopped.</summary>
        internal void OnCollecting(bool on)
        {
            if (on)
            {
                if (_journal == null)
                {
                    _journal = new CrashJournal(_dir, _inputs);
                    _journal.Start(_useThreads);
                }
                if (!_reportStarted)
                {
                    _reportStarted = true;
                    StartReport();
                }
                return;
            }
            StopJournal(true);
        }

        private void StartReport()
        {
            _reporter = new CrashReporter(_dir, _source, _enqueue, _allowed, _waitPersisted)
            {
                CurrentDeviceId = _inputs.DeviceId,
                UnityTombstoneDir = _inputs.UnityTombstoneDir,
            };
            if (!_useThreads)
            {
                _reporter.ProcessPrevious(_inputs.Pid, _inputs.ProcessStartMs);
                return;
            }
            try
            {
                var reporter = _reporter;
                int pid = _inputs.Pid;
                long start = _inputs.ProcessStartMs;
                var t = new Thread(() => reporter.ProcessPrevious(pid, start))
                {
                    IsBackground = true,
                    Name = "OzxApm.CrashReport"
                };
                t.Start();
                _reportThread = t;
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashMonitor.StartReport", ex);
            }
        }

        /// <summary>Waits for the report of earlier processes (tests).</summary>
        internal bool WaitReport(int timeoutMs)
        {
            var t = _reportThread;
            return t == null || t.Join(timeoutMs);
        }

        internal void StopJournal(bool deleteFiles)
        {
            var j = _journal;
            _journal = null;
            j?.Stop(deleteFiles);
        }

        internal void OnPause(bool paused)
        {
            _inputs.Paused = paused;
            CrashJournal.NotifyChanged();
        }

        internal void OnScene(string scene)
        {
            _inputs.Scene = scene ?? "";
            CrashJournal.NotifyChanged();
        }

        internal void OnNetType(string netType)
        {
            if (!ReferenceEquals(_inputs.NetType, netType))
            {
                _inputs.NetType = netType ?? "";
                CrashJournal.NotifyChanged();
            }
        }

        /// <summary>Consent Denied: everything in the directory goes (also at Initialize).</summary>
        internal void DeleteAll()
        {
            StopJournal(true);
            DeleteDirectory(_dir);
        }

        /// <summary>
        /// Deletes the directory at once, on the calling thread: it holds a few
        /// small files (two per process, at most a handful of processes), and
        /// deleting later would race the next journal and report (a new
        /// journal unlinked while written, old-id crashes reported after a reset).
        /// </summary>
        internal static void DeleteDirectory(string dir)
        {
            if (string.IsNullOrEmpty(dir))
                return;
            CrashJournal.DeleteAll(dir);
        }

        /// <summary>ResetDeviceId: journals of earlier processes carry the old id.</summary>
        internal void OnDeviceIdReset()
        {
            // Synchronous (a few small files): a report still running reads the
            // directory too, and also skips journals of another device id.
            var own = _journal?.Paths;
            try
            {
                if (System.IO.Directory.Exists(_dir))
                {
                    foreach (var f in System.IO.Directory.GetFiles(_dir))
                    {
                        if (own != null && (f == own[0] || f == own[1]))
                            continue;
                        File.Delete(f);
                    }
                }
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("CrashMonitor.OnDeviceIdReset", ex);
            }
            CrashJournal.NotifyChanged();
        }

        /// <summary>Clean shutdown: not a crash.</summary>
        internal void OnShutdown()
        {
            StopJournal(true);
        }
    }
}
