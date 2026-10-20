using System;
using System.Globalization;
using System.IO;
using System.Text;

namespace OzxApm.Network
{
    /// <summary>Where a session file upload is (APM-13).</summary>
    public enum SessionUploadState
    {
        /// <summary>Waiting to be zipped.</summary>
        Queued,
        /// <summary>Zipped; the upload has not been created on the server yet.</summary>
        Packed,
        /// <summary>Created on the server; parts are being sent.</summary>
        Uploading,
        /// <summary>The server has it (complete); the local copy is deleted.</summary>
        Done,
        /// <summary>Given up (the reason is in Error); the local copy is deleted.</summary>
        Failed
    }

    /// <summary>
    /// The persisted state of one session file upload: <c>&lt;id&gt;.state</c>
    /// next to <c>&lt;id&gt;.zip</c> in the SDK's session file directory, one
    /// <c>key=value</c> per line (values escaped), rewritten atomically (temp file
    /// + replace) after every step, so an upload resumes after the app is killed.
    /// Worker thread only.
    /// </summary>
    internal sealed class SessionFileRecord
    {
        public string Id;
        public string Directory;
        public string Recording;
        public SessionUploadState State;
        public long CreatedAtMs;

        // Captured when Upload was called (main thread).
        public string DeviceId;
        public string SessionId;
        /// <summary>The APM session when Upload was called (used when neither the options nor session.json name one).</summary>
        public string CurrentSession;
        public string GitCommit;
        public string AppVersion;
        public string Build;
        public string BuildType;
        public string Platform;
        public string DeviceModel;
        public string OsVersion;
        public string UnityVersion;

        // Set when zipped.
        public long Size;
        public string Sha256;
        public int PartBytes;
        public int PartCount;
        public int FileCount;
        public long RawBytes;
        /// <summary>One character per part: '1' acknowledged by the server.</summary>
        public string Acked = "";
        public int PackAttempts;
        public string Error;

        public int AckedCount
        {
            get
            {
                int n = 0;
                foreach (char c in Acked)
                {
                    if (c == '1')
                        n++;
                }
                return n;
            }
        }

        public int NextMissingPart()
        {
            for (int i = 0; i < PartCount; i++)
            {
                if (i >= Acked.Length || Acked[i] != '1')
                    return i;
            }
            return -1;
        }

        public void SetAcked(int part)
        {
            var chars = (Acked ?? "").PadRight(PartCount, '0').ToCharArray();
            if (part >= 0 && part < chars.Length)
                chars[part] = '1';
            Acked = new string(chars);
        }

        public string Serialize()
        {
            var sb = new StringBuilder(512);
            void Put(string k, string v)
            {
                if (v == null)
                    return;
                sb.Append(k).Append('=').Append(Escape(v)).Append('\n');
            }
            Put("version", "1");
            Put("id", Id);
            Put("directory", Directory);
            Put("recording", Recording);
            Put("state", State.ToString());
            Put("created", CreatedAtMs.ToString(CultureInfo.InvariantCulture));
            Put("device", DeviceId);
            Put("session", SessionId);
            Put("currentSession", CurrentSession);
            Put("commit", GitCommit);
            Put("appVersion", AppVersion);
            Put("build", Build);
            Put("buildType", BuildType);
            Put("platform", Platform);
            Put("deviceModel", DeviceModel);
            Put("os", OsVersion);
            Put("unity", UnityVersion);
            Put("size", Size.ToString(CultureInfo.InvariantCulture));
            Put("sha256", Sha256);
            Put("partBytes", PartBytes.ToString(CultureInfo.InvariantCulture));
            Put("partCount", PartCount.ToString(CultureInfo.InvariantCulture));
            Put("files", FileCount.ToString(CultureInfo.InvariantCulture));
            Put("raw", RawBytes.ToString(CultureInfo.InvariantCulture));
            Put("acked", Acked);
            Put("packAttempts", PackAttempts.ToString(CultureInfo.InvariantCulture));
            Put("error", Error);
            return sb.ToString();
        }

        public static SessionFileRecord Parse(string text)
        {
            var r = new SessionFileRecord();
            foreach (var line in text.Split('\n'))
            {
                int eq = line.IndexOf('=');
                if (eq <= 0)
                    continue;
                string k = line.Substring(0, eq);
                string v = Unescape(line.Substring(eq + 1));
                switch (k)
                {
                    case "id": r.Id = v; break;
                    case "directory": r.Directory = v; break;
                    case "recording": r.Recording = v; break;
                    case "state":
                        if (Enum.TryParse(v, out SessionUploadState s))
                            r.State = s;
                        break;
                    case "created": long.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.CreatedAtMs); break;
                    case "device": r.DeviceId = v; break;
                    case "session": r.SessionId = v; break;
                    case "currentSession": r.CurrentSession = v; break;
                    case "commit": r.GitCommit = v; break;
                    case "appVersion": r.AppVersion = v; break;
                    case "build": r.Build = v; break;
                    case "buildType": r.BuildType = v; break;
                    case "platform": r.Platform = v; break;
                    case "deviceModel": r.DeviceModel = v; break;
                    case "os": r.OsVersion = v; break;
                    case "unity": r.UnityVersion = v; break;
                    case "size": long.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.Size); break;
                    case "sha256": r.Sha256 = v; break;
                    case "partBytes": int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.PartBytes); break;
                    case "partCount": int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.PartCount); break;
                    case "files": int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.FileCount); break;
                    case "raw": long.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.RawBytes); break;
                    case "acked": r.Acked = v; break;
                    case "packAttempts": int.TryParse(v, NumberStyles.Integer, CultureInfo.InvariantCulture, out r.PackAttempts); break;
                    case "error": r.Error = v; break;
                }
            }
            return string.IsNullOrEmpty(r.Id) ? null : r;
        }

        private static string Escape(string v)
        {
            return v.Replace("\\", "\\\\").Replace("\n", "\\n").Replace("\r", "\\r");
        }

        private static string Unescape(string v)
        {
            if (v.IndexOf('\\') < 0)
                return v;
            var sb = new StringBuilder(v.Length);
            for (int i = 0; i < v.Length; i++)
            {
                char c = v[i];
                if (c == '\\' && i + 1 < v.Length)
                {
                    char n = v[++i];
                    sb.Append(n == 'n' ? '\n' : n == 'r' ? '\r' : n);
                }
                else
                {
                    sb.Append(c);
                }
            }
            return sb.ToString();
        }

        /// <summary>Writes the record atomically (temp file, then replace).</summary>
        public void Save(string path)
        {
            string tmp = path + ".tmp";
            File.WriteAllText(tmp, Serialize(), new UTF8Encoding(false));
            if (File.Exists(path))
                File.Delete(path);
            File.Move(tmp, path);
        }

        public static SessionFileRecord Load(string path)
        {
            try
            {
                return Parse(File.ReadAllText(path, Encoding.UTF8));
            }
            catch (IOException)
            {
                return null;
            }
        }
    }
}
