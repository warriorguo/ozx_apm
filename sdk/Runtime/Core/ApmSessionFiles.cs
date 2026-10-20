using System;
using OzxApm.Network;

namespace OzxApm.Core
{
    /// <summary>Optional facts of a session file upload.</summary>
    public sealed class SessionUploadOptions
    {
        /// <summary>
        /// The build's git commit (the player cannot read .git: bake it into the
        /// build). Default: the game context key "gitCommit", else the server
        /// reads session.json's gitCommit.
        /// </summary>
        public string GitCommit;

        /// <summary>
        /// The APM session the recording belongs to (ApmClient.GetSessionId()
        /// when the recording started). Default: session.json's "apmSessionId",
        /// else the current APM session.
        /// </summary>
        public string ApmSessionId;
    }

    /// <summary>
    /// Uploads AiTelemetry session directories (OZX's
    /// <c>SessionFolder/&lt;session&gt;/</c>) recorded by development builds to
    /// the APM server, for verify-ticket (APM-13; RELEASE.md, README).
    ///
    /// <b>Development and editor builds only.</b> In a release player the code
    /// is compiled out (<see cref="CompiledIn"/>: no thread, no file, no request
    /// ever) and every call returns null / false; at run time it also needs the
    /// engine to say it is the Editor or a Development Build, and the SDK's build
    /// type dev or editor. The server refuses any other build type too.
    ///
    /// Call <see cref="Upload"/> once the session directory is complete (after
    /// AiTelemetry ended the session, or at the next launch for the previous
    /// one). The SDK zips a copy in the background, queues it on disk and sends
    /// it in parts when online (Wi-Fi only by default), resuming after a
    /// restart; it never changes or deletes the directory. Technical consent is
    /// required to send (Denied deletes the queued copies). Thread-safe.
    /// </summary>
    public static class ApmSessionFiles
    {
        /// <summary>
        /// True in the Editor and in development players (DEVELOPMENT_BUILD);
        /// false in release players, where the uploader is never created.
        /// </summary>
        public const bool CompiledIn =
#if UNITY_EDITOR || DEVELOPMENT_BUILD
            true;
#else
            false;
#endif

        /// <summary>
        /// The run-time gate: the code is compiled in, the engine runs the Editor
        /// or a Development Build, and the SDK's build type is dev or editor.
        /// </summary>
        internal static bool Allowed(string buildTypeName, bool isEditor, bool isDebugBuild)
        {
#if UNITY_EDITOR || DEVELOPMENT_BUILD
            return (isEditor || isDebugBuild) && (buildTypeName == "dev" || buildTypeName == "editor");
#else
            return false;
#endif
        }

        /// <summary>Uploads can be queued now (initialized, allowed, enabled).</summary>
        public static bool IsAvailable => ApmClient.SessionFileUploader != null;

        /// <summary>
        /// Queues the session directory for upload and returns its upload id
        /// (shown in the APM admin), or null when not possible: a release build,
        /// before Initialize, ApmConfig.EnableSessionFileUpload off, technical
        /// consent denied, too many uploads pending, or a directory name that
        /// is not a session's (the reason is logged).
        /// </summary>
        public static string Upload(string sessionDirectory, SessionUploadOptions options = null)
        {
#if UNITY_EDITOR || DEVELOPMENT_BUILD
            return ApmClient.EnqueueSessionFiles(sessionDirectory, options);
#else
            return null;
#endif
        }

        /// <summary>The uploads of this launch and those waiting (copies; allocates).</summary>
        public static SessionUploadInfo[] GetUploads()
        {
            var uploader = ApmClient.SessionFileUploader;
            return uploader != null ? uploader.GetUploads() : Array.Empty<SessionUploadInfo>();
        }
    }
}
