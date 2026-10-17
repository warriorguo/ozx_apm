using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Parses exceptions and error logs and computes their fingerprint (APM-4).
    ///
    /// This is a port of the server's algorithm (server/internal/fingerprint,
    /// whose package comment documents it; version <see cref="Version"/>). The
    /// server recomputes the fingerprint from the raw text it receives and is
    /// authoritative; the SDK uses its own result only to de-duplicate repeats
    /// on the device, so both must group alike. Both are checked against
    /// server/internal/fingerprint/testdata/vectors.json.
    ///
    /// The SDK also uses it to find an exception embedded in an error log's
    /// message (OZX's GameEventBus logs "... Error handling event X: {ex}")
    /// before the message is cut to 500 characters, so that the exception's
    /// type, frames and inner exceptions reach the server.
    ///
    /// Pure C# (no UnityEngine, no Regex), safe under IL2CPP and code
    /// stripping. It allocates, so it runs only on the first occurrence of an
    /// error in a de-duplication window. Every decision is made on ASCII
    /// characters, which keeps it identical to the Go version (which sees UTF-8
    /// bytes where C# sees UTF-16 units).
    /// </summary>
    internal static class ExceptionParser
    {
        internal const int Version = 1;

        internal const string LogTypeException = "exception";
        internal const string LogTypeError = "error";
        internal const string LogTypeAssert = "assert";

        // Go caps every input at 64 KiB of UTF-8; the SDK never passes more
        // than 8000 characters, where both caps are no-ops.
        private const int MaxParseChars = 64 * 1024;
        private const int MaxFramesPerException = 200;
        private const int MaxChain = 20;
        private const int FingerprintFrames = 2;
        private const int MaxEmbeddedCandidates = 8;
        private const int TemplateSourceLength = 300;
        private const int TemplateLength = 200;

        private const string EndOfInnerMarker = "--- End of inner exception stack trace ---";
        private const string EndOfStackMarker = "--- End of stack trace from previous location";
        private const string AggregateInnerTag = "---> (Inner Exception #";
        private const string AggregateInnerEnd = "<---";
        private const string InnerSeparator = " ---> ";
        private const string UnityRethrowAs = "Rethrow as ";

        private static readonly string[] NotInAppPrefixes =
        {
            "System.", "Mono.", "Microsoft.", "UnityEngine.", "UnityEditor.", "Unity.",
            "TMPro.", "Newtonsoft.", "OzxApm.",
        };

        internal sealed class Frame
        {
            public string Method;
            public string File = "";
            public int Line;
            public bool InApp;
        }

        internal sealed class ExceptionInfo
        {
            public string Type = "";
            public string Message = "";
            public List<Frame> Frames = new List<Frame>();
        }

        internal sealed class Result
        {
            public string Kind;
            public bool Wrapped;
            public List<ExceptionInfo> Chain = new List<ExceptionInfo>();
            public List<Frame> LogFrames = new List<Frame>();
            public string MessageTemplate = "";
            public string ThrowSite = "";
            public string CallSite = "";
            public string Fingerprint;

            public string Type => Chain.Count > 0 ? Chain[0].Type : "";
        }

        /// <summary>
        /// Parses one event and computes its fingerprint. logType is
        /// "exception", "error" or "assert" ("" only for events of SDKs before
        /// APM-4, which the server handles; kept for the shared test vectors).
        /// </summary>
        internal static Result Analyze(string logType, string message, string stack, string embedded)
        {
            string lt = logType == LogTypeException || logType == LogTypeError || logType == LogTypeAssert ? logType : "";
            bool legacy = string.IsNullOrEmpty(logType);
            string msg = Cap(message ?? "");
            string stk = Cap(stack ?? "");
            string emb = Cap(embedded ?? "");
            if (legacy)
                msg = DropTruncatedLine(msg);

            var r = new Result();
            if (lt == LogTypeException)
            {
                r.Kind = LogTypeException;
                r.Chain = ParseUnityException(msg, stk);
            }
            else if (emb.Length > 0)
            {
                r.Kind = LogTypeException;
                r.Wrapped = true;
                r.Chain = ParseExceptionText(emb);
                r.LogFrames = ParseStackFrames(stk);
            }
            else if (legacy && StartsWithExceptionType(msg))
            {
                r.Kind = LogTypeException;
                r.Chain = ParseUnityException(msg, stk);
            }
            else
            {
                int i = legacy ? FindEmbedded(msg) : -1;
                if (i >= 0)
                {
                    r.Kind = LogTypeException;
                    r.Wrapped = true;
                    r.Chain = ParseExceptionText(msg.Substring(i));
                }
                else
                {
                    r.Kind = lt == LogTypeAssert ? LogTypeAssert : LogTypeError;
                }
                r.LogFrames = ParseStackFrames(stk);
            }

            if (r.Chain.Count > 0)
            {
                r.MessageTemplate = Template(r.Chain[0].Message);
                r.ThrowSite = Site(r.Chain[0].Frames);
            }
            else
            {
                r.MessageTemplate = Template(msg);
            }
            r.CallSite = Site(r.LogFrames);
            r.Fingerprint = Hash(Canonical(r));
            return r;
        }

        /// <summary>"Type: first line of message" of every inner exception, outermost first.</summary>
        internal static List<string> InnerExceptions(Result r)
        {
            var list = new List<string>();
            for (int i = 1; i < r.Chain.Count; i++)
            {
                string s = r.Chain[i].Type;
                string m = FirstLine(r.Chain[i].Message);
                if (m.Length > 0)
                    s += ": " + m;
                list.Add(s);
            }
            return list;
        }

        private static string Canonical(Result r)
        {
            var b = new StringBuilder(256);
            b.Append('v').Append(Version.ToString(CultureInfo.InvariantCulture)).Append('\n');
            if (r.Chain.Count > 0)
            {
                var outer = r.Chain[0];
                b.Append(LogTypeException).Append('\n');
                b.Append(ShortType(outer.Type)).Append('\n');
                for (int i = 1; i < r.Chain.Count; i++)
                {
                    if (i > 1)
                        b.Append('>');
                    b.Append(ShortType(r.Chain[i].Type));
                }
                b.Append('\n');
                var frames = TopFrames(outer.Frames, FingerprintFrames, true);
                if (frames.Count == 0)
                    frames = TopFrames(outer.Frames, FingerprintFrames, false);
                if (frames.Count > 0)
                    b.Append("f:").Append(string.Join("\n", frames.ToArray()));
                else
                    b.Append("m:").Append(r.MessageTemplate);
                return b.ToString();
            }
            b.Append(r.Kind).Append('\n');
            b.Append("m:").Append(r.MessageTemplate).Append('\n');
            b.Append("f:").Append(string.Join("\n", TopFrames(r.LogFrames, 1, true).ToArray()));
            return b.ToString();
        }

        private static List<string> TopFrames(List<Frame> frames, int n, bool inApp)
        {
            var list = new List<string>(n);
            foreach (var f in frames)
            {
                if (list.Count == n)
                    break;
                if (inApp && !f.InApp)
                    continue;
                if (list.Count > 0 && list[list.Count - 1] == f.Method)
                    continue;
                list.Add(f.Method);
            }
            return list;
        }

        private static string Site(List<Frame> frames)
        {
            foreach (var f in frames)
            {
                if (f.InApp)
                    return FormatFrame(f);
            }
            return "";
        }

        internal static string FormatFrame(Frame f)
        {
            if (f.File.Length == 0)
                return f.Method;
            if (f.Line > 0)
                return f.Method + " (at " + f.File + ":" + f.Line.ToString(CultureInfo.InvariantCulture) + ")";
            return f.Method + " (at " + f.File + ")";
        }

        private static string DropTruncatedLine(string msg)
        {
            if (Encoding.UTF8.GetByteCount(msg) < 497 || !msg.EndsWith("...", StringComparison.Ordinal))
                return msg;
            int i = msg.LastIndexOf('\n');
            return i > 0 ? msg.Substring(0, i) : msg;
        }

        private static bool StartsWithExceptionType(string msg)
        {
            var h = ParseHeader(msg, out bool ok);
            return ok && ShortType(h.Type).EndsWith("Exception", StringComparison.Ordinal);
        }

        private static string Cap(string s)
        {
            if (s.Length <= MaxParseChars)
                return s;
            int n = MaxParseChars;
            if (char.IsHighSurrogate(s[n - 1]))
                n--;
            return s.Substring(0, n);
        }

        private static string FirstLine(string s)
        {
            int i = s.IndexOf('\n');
            if (i >= 0)
                s = s.Substring(0, i);
            return s.TrimEnd('\r', ' ', '\t');
        }

        private static string[] SplitLines(string s)
        {
            var lines = s.Split('\n');
            for (int i = 0; i < lines.Length; i++)
            {
                if (lines[i].EndsWith("\r", StringComparison.Ordinal))
                    lines[i] = lines[i].Substring(0, lines[i].Length - 1);
            }
            return lines;
        }

        // ---- parsing ----

        private static List<ExceptionInfo> ParseExceptionText(string text)
        {
            var lines = SplitLines(text);
            int i = 0;
            var header = new List<string>();
            for (; i < lines.Length; i++)
            {
                if (IsMarker(lines[i]) || ParseFrame(lines[i]) != null)
                    break;
                header.Add(lines[i]);
            }
            var chain = new List<ExceptionInfo>();
            foreach (var part in Split(string.Join("\n", header.ToArray()), InnerSeparator))
            {
                if (chain.Count == MaxChain)
                    break;
                chain.Add(ParseHeader(part, out _));
            }

            var sections = new List<List<Frame>> { new List<Frame>() };
            var extras = new List<ExceptionInfo>();
            bool inExtra = false;
            for (; i < lines.Length; i++)
            {
                string t = lines[i].Trim();
                if (t.Length == 0)
                    continue;
                if (t.StartsWith(EndOfInnerMarker, StringComparison.Ordinal))
                {
                    if (!inExtra)
                        sections.Add(new List<Frame>());
                    continue;
                }
                if (t.StartsWith(EndOfStackMarker, StringComparison.Ordinal))
                    continue;
                if (t.StartsWith(AggregateInnerTag, StringComparison.Ordinal))
                {
                    inExtra = true;
                    string rest = t.Substring(AggregateInnerTag.Length);
                    int j = rest.IndexOf(") ", StringComparison.Ordinal);
                    if (j >= 0)
                        rest = rest.Substring(j + 2);
                    bool closed = rest.EndsWith(AggregateInnerEnd, StringComparison.Ordinal);
                    if (closed)
                        rest = rest.Substring(0, rest.Length - AggregateInnerEnd.Length);
                    var h = ParseHeader(rest.Trim(), out _);
                    if (extras.Count < MaxChain)
                        extras.Add(h);
                    if (closed)
                        inExtra = false;
                    continue;
                }
                bool ended = false;
                if (inExtra && t.EndsWith(AggregateInnerEnd, StringComparison.Ordinal))
                {
                    t = t.Substring(0, t.Length - AggregateInnerEnd.Length).Trim();
                    ended = true;
                }
                var f = ParseFrame(t);
                if (f != null)
                {
                    if (inExtra)
                    {
                        if (extras.Count > 0 && extras[extras.Count - 1].Frames.Count < MaxFramesPerException)
                            extras[extras.Count - 1].Frames.Add(f);
                    }
                    else
                    {
                        var s = sections[sections.Count - 1];
                        if (s.Count < MaxFramesPerException)
                            s.Add(f);
                    }
                }
                if (ended)
                    inExtra = false;
            }

            int n = chain.Count, m = sections.Count;
            for (int k = 0; k < m; k++)
            {
                var section = sections[m - 1 - k];
                int target = k < n ? k : n - 1;
                section.AddRange(chain[target].Frames);
                chain[target].Frames = section;
            }
            foreach (var e in extras)
            {
                if (chain.Count == MaxChain)
                    break;
                bool dup = false;
                foreach (var c in chain)
                {
                    if (c.Type == e.Type && c.Message == e.Message)
                    {
                        dup = true;
                        break;
                    }
                }
                if (!dup)
                    chain.Add(e);
            }
            return chain;
        }

        // Unity's form (UnityEngine.StackTraceUtility): the message is the
        // innermost exception, each outer one follows "Rethrow as Type: message".
        private static List<ExceptionInfo> ParseUnityException(string message, string stack)
        {
            var innerToOuter = new List<ExceptionInfo> { ParseHeader(message, out _) };
            var sections = new List<List<Frame>> { new List<Frame>() };
            foreach (var l in SplitLines(stack))
            {
                string t = l.Trim();
                if (t.StartsWith(UnityRethrowAs, StringComparison.Ordinal))
                {
                    if (innerToOuter.Count < MaxChain)
                    {
                        innerToOuter.Add(ParseHeader(t.Substring(UnityRethrowAs.Length), out _));
                        sections.Add(new List<Frame>());
                    }
                    continue;
                }
                var f = ParseFrame(t);
                if (f != null)
                {
                    var s = sections[sections.Count - 1];
                    if (s.Count < MaxFramesPerException)
                        s.Add(f);
                }
            }
            var chain = new List<ExceptionInfo>(innerToOuter.Count);
            for (int k = innerToOuter.Count - 1; k >= 0; k--)
            {
                innerToOuter[k].Frames = sections[k];
                chain.Add(innerToOuter[k]);
            }
            return chain;
        }

        private static List<Frame> ParseStackFrames(string stack)
        {
            var frames = new List<Frame>();
            foreach (var l in SplitLines(stack))
            {
                if (frames.Count == MaxFramesPerException)
                    break;
                var f = ParseFrame(l);
                if (f != null)
                    frames.Add(f);
            }
            return frames;
        }

        private static bool IsMarker(string line)
        {
            string t = line.Trim();
            return t.StartsWith(EndOfInnerMarker, StringComparison.Ordinal) ||
                   t.StartsWith(EndOfStackMarker, StringComparison.Ordinal) ||
                   t.StartsWith(AggregateInnerTag, StringComparison.Ordinal);
        }

        // Like Go's strings.Split: separators do not overlap, "" gives one empty part.
        private static List<string> Split(string s, string sep)
        {
            var parts = new List<string>();
            int start = 0;
            while (true)
            {
                int i = s.IndexOf(sep, start, StringComparison.Ordinal);
                if (i < 0)
                    break;
                parts.Add(s.Substring(start, i - start));
                start = i + sep.Length;
            }
            parts.Add(s.Substring(start));
            return parts;
        }

        private static ExceptionInfo ParseHeader(string text, out bool ok)
        {
            text = text.Trim();
            string first = text, rest = "";
            int nl = text.IndexOf('\n');
            if (nl >= 0)
            {
                first = text.Substring(0, nl).TrimEnd('\r');
                rest = text.Substring(nl + 1);
            }
            int i = first.IndexOf(": ", StringComparison.Ordinal);
            if (i > 0 && IsTypeName(first.Substring(0, i)))
            {
                ok = true;
                return new ExceptionInfo { Type = first.Substring(0, i), Message = text.Substring(i + 2).Trim() };
            }
            string t = first.Trim();
            if (t.EndsWith(":", StringComparison.Ordinal))
                t = t.Substring(0, t.Length - 1);
            if (IsTypeName(t))
            {
                ok = true;
                return new ExceptionInfo { Type = t, Message = rest.Trim() };
            }
            ok = false;
            return new ExceptionInfo { Message = text };
        }

        private static bool IsTypeName(string s)
        {
            return IsTypeName(s, 0, s.Length);
        }

        private static bool IsTypeName(string str, int s, int e)
        {
            if (e == s || e - s > 512)
                return false;
            for (int i = s; i < e; i++)
            {
                char c = str[i];
                if (IsLetter(c) || c == '_' || c >= 0x80)
                    continue;
                if (IsDigit(c) || c == '.' || c == '+' || c == '`')
                {
                    if (i == s)
                        return false;
                    continue;
                }
                return false;
            }
            return true;
        }

        /// <summary>
        /// Index in msg where an embedded Exception.ToString() starts (a type
        /// name ending in "Exception", followed by stack frames or an inner
        /// exception), or -1.
        /// </summary>
        internal static int FindEmbedded(string msg)
        {
            if (string.IsNullOrEmpty(msg))
                return -1;
            msg = Cap(msg);
            int from = 0;
            for (int tries = 0; tries < MaxEmbeddedCandidates;)
            {
                int j = msg.IndexOf("Exception", from, StringComparison.Ordinal);
                if (j < 0)
                    return -1;
                int end = j + "Exception".Length;
                from = end;
                if (end < msg.Length)
                {
                    char c = msg[end];
                    if (c != ':' && c != '\r' && c != '\n' && string.CompareOrdinal(msg, end, InnerSeparator, 0, InnerSeparator.Length) != 0)
                        continue;
                }
                int s = j;
                while (s > 0 && IsTypeChar(msg[s - 1]))
                    s--;
                if (!IsTypeName(msg, s, end))
                    continue;
                if (s > 0 && !IsBoundary(msg[s - 1]))
                    continue;
                tries++;
                if (HasExceptionBody(msg, s))
                    return s;
            }
            return -1;
        }

        // Whether the text from start continues like Exception.ToString(): a
        // stack frame on some line, an inner exception (" ---> " before the
        // first frame or marker line), or an AggregateException inner block.
        private static bool HasExceptionBody(string text, int start)
        {
            bool inHeader = true;
            int pos = start;
            while (pos < text.Length)
            {
                int nl = text.IndexOf('\n', pos);
                int lineStart = pos, end = nl < 0 ? text.Length : nl;
                int s = lineStart, e = end;
                pos = nl < 0 ? text.Length : nl + 1;
                TrimRange(text, ref s, ref e);
                if (StartsAt(text, s, e, AggregateInnerTag))
                    return true;
                if (StartsAt(text, s, e, EndOfInnerMarker) || StartsAt(text, s, e, EndOfStackMarker))
                {
                    inHeader = false;
                    continue;
                }
                if (inHeader && IndexOfIn(text, lineStart, end, InnerSeparator) >= 0)
                    return true;
                if (ParseFrame(text, s, e) != null)
                    return true;
            }
            return false;
        }

        private static bool IsTypeChar(char c)
        {
            return IsLetter(c) || IsDigit(c) || c == '_' || c == '.' || c == '+' || c == '`';
        }

        private static bool IsBoundary(char c)
        {
            switch (c)
            {
                case ' ': case '\t': case '\r': case '\n': case ':': case '(': case '[': case '{': case '"': case '\'': case '=':
                    return true;
            }
            return false;
        }

        // Range helpers: [s, e) of a string, so that a line is parsed without
        // allocating substrings for the parts that are only looked at.

        private static void TrimRange(string str, ref int s, ref int e)
        {
            while (s < e && char.IsWhiteSpace(str[s]))
                s++;
            while (e > s && char.IsWhiteSpace(str[e - 1]))
                e--;
        }

        private static bool StartsAt(string str, int s, int e, string prefix)
        {
            return e - s >= prefix.Length && string.CompareOrdinal(str, s, prefix, 0, prefix.Length) == 0;
        }

        private static int IndexOfIn(string str, int s, int e, string value)
        {
            return e - s < value.Length ? -1 : str.IndexOf(value, s, e - s, StringComparison.Ordinal);
        }

        private static int LastIndexOfIn(string str, int s, int e, string value)
        {
            for (int i = e - value.Length; i >= s; i--)
            {
                if (string.CompareOrdinal(str, i, value, 0, value.Length) == 0)
                    return i;
            }
            return -1;
        }

        /// <summary>Parses one stack line in any supported format; null if it is not a frame.</summary>
        internal static Frame ParseFrame(string line)
        {
            return ParseFrame(line, 0, line.Length);
        }

        private static Frame ParseFrame(string str, int s, int e)
        {
            TrimRange(str, ref s, ref e);
            bool isAt = false;
            if (StartsAt(str, s, e, "at "))
            {
                s += 3;
                TrimRange(str, ref s, ref e);
                isAt = true;
            }
            if (StartsAt(str, s, e, "(wrapper "))
            {
                int w = IndexOfIn(str, s, e, ") ");
                if (w < 0)
                    return null;
                s = w + 2;
                TrimRange(str, ref s, ref e);
            }
            if (s == e || StartsAt(str, s, e, "...") || StartsAt(str, s, e, UnityRethrowAs))
                return null;
            int ms = s, me, rs = e, re = e;
            int open = str.IndexOf('(', s, e - s);
            if (open >= 0)
            {
                me = open;
                TrimRange(str, ref ms, ref me);
                int close = MatchParen(str, open, e);
                if (close >= 0)
                {
                    rs = close + 1;
                    TrimRange(str, ref rs, ref re);
                }
            }
            else
            {
                if (!isAt)
                    return null;
                me = e;
                int sp = str.IndexOf(' ', s, e - s);
                if (sp >= 0)
                {
                    me = sp;
                    rs = sp + 1;
                    TrimRange(str, ref rs, ref re);
                }
            }
            if (!LooksLikeMethod(str, ms, me))
                return null;
            string norm = NormalizeMethod(str, ms, me);
            if (norm.Length == 0)
                return null;
            var f = new Frame { Method = norm, InApp = IsInApp(norm) };
            ParseLocation(str, rs, re, f);
            return f;
        }

        private static int MatchParen(string str, int open, int e)
        {
            int depth = 0;
            for (int i = open; i < e; i++)
            {
                if (str[i] == '(')
                    depth++;
                else if (str[i] == ')' && --depth == 0)
                    return i;
            }
            return -1;
        }

        private static bool LooksLikeMethod(string str, int s, int e)
        {
            if (e == s || e - s > 1024 || IndexOfIn(str, s, e, ": ") >= 0)
                return false;
            int depth = 0;
            bool sep = false;
            for (int i = s; i < e; i++)
            {
                char c = str[i];
                if (c == '[' || c == '<')
                    depth++;
                else if (c == ']' || c == '>')
                {
                    if (depth > 0)
                        depth--;
                }
                else if (depth > 0)
                {
                }
                else if (c == '.' || c == ':')
                    sep = true;
                else if (IsLetter(c) || IsDigit(c) || c >= 0x80)
                {
                }
                else if (c == '_' || c == '+' || c == '/' || c == '`' || c == '|' || c == '$' || c == '@' || c == '-' || c == ',')
                {
                }
                else
                    return false;
            }
            return sep;
        }

        private static void ParseLocation(string str, int s, int e, Frame f)
        {
            if (StartsAt(str, s, e, "[0x") || StartsAt(str, s, e, "<0x"))
            {
                // Mono / IL2CPP: "[0x0001a] in /path/File.cs:42", "in <hash>:0"
                int i = IndexOfIn(str, s, e, " in ");
                if (i >= 0)
                    SplitFileLine(str, i + 4, e, f);
            }
            else if (StartsAt(str, s, e, "in "))
            {
                // .NET: "in C:\path\File.cs:line 42"
                int ls = s + 3, le = e;
                TrimRange(str, ref ls, ref le);
                int k = LastIndexOfIn(str, ls, le, ":line ");
                if (k >= 0)
                {
                    int ns = k + 6, ne = le;
                    TrimRange(str, ref ns, ref ne);
                    Atoi(str, ns, ne, out f.Line);
                    f.File = CleanFile(str, ls, k);
                }
                else
                {
                    SplitFileLine(str, ls, le, f);
                }
            }
            else if (StartsAt(str, s, e, "(at ") && str[e - 1] == ')')
            {
                // Unity: "(at Assets/File.cs:42)"
                SplitFileLine(str, s + 4, e - 1, f);
            }
        }

        private static void SplitFileLine(string str, int s, int e, Frame f)
        {
            TrimRange(str, ref s, ref e);
            int i = e - 1;
            while (i >= s && str[i] != ':')
                i--;
            if (i >= s && Atoi(str, i + 1, e, out int n))
            {
                f.File = CleanFile(str, s, i);
                f.Line = n;
                return;
            }
            f.File = CleanFile(str, s, e);
            f.Line = 0;
        }

        // Drops placeholders such as "<00000000000000000000000000000000>" (no
        // symbols) and "<filename unknown>".
        private static string CleanFile(string str, int s, int e)
        {
            TrimRange(str, ref s, ref e);
            if (e > s && str[s] == '<' && str[e - 1] == '>')
                return "";
            return str.Substring(s, e - s);
        }

        private static bool Atoi(string str, int s, int e, out int n)
        {
            n = 0;
            if (e == s || e - s > 9)
                return false;
            int v = 0;
            for (int i = s; i < e; i++)
            {
                char c = str[i];
                if (!IsDigit(c))
                    return false;
                v = v * 10 + (c - '0');
            }
            n = v;
            return true;
        }

        private static bool IsInApp(string method)
        {
            foreach (var p in NotInAppPrefixes)
            {
                if (method.StartsWith(p, StringComparison.Ordinal))
                    return false;
            }
            return true;
        }

        // ---- normalization ----

        /// <summary>Method identity: "Ns.Type:Publish&lt;T&gt;" and "Ns.Type.Publish[T]" are "Ns.Type.Publish".</summary>
        internal static string NormalizeMethod(string m)
        {
            return NormalizeMethod(m, 0, m.Length);
        }

        private static string NormalizeMethod(string str, int s, int e)
        {
            bool plain = true;
            for (int i = s; i < e && plain; i++)
            {
                char c = str[i];
                plain = c != '[' && c != '<' && c != '`' && c != ' ' && c != '\t' && c != ':' && c != '/';
            }
            if (plain)
                return str.Substring(s, e - s); // nothing to change (and no compiler-generated name)

            var b = new StringBuilder(e - s);
            char prev = '\0';
            for (int i = s; i < e; i++)
            {
                char c = str[i];
                if (c == '[')
                {
                    i = SkipBracket(str, i, e, '[', ']');
                    continue;
                }
                if (c == '<' && (IsAlnum(prev) || prev == '_' || prev >= 0x80))
                {
                    i = SkipBracket(str, i, e, '<', '>');
                    continue;
                }
                if (c == '`')
                {
                    while (i + 1 < e && IsDigit(str[i + 1]))
                        i++;
                    continue;
                }
                if (c == ' ' || c == '\t')
                    continue;
                if (c == ':')
                    c = '.';
                else if (c == '/')
                    c = '+';
                b.Append(c);
                prev = c;
            }
            return TrimGeneratedSuffixes(b.ToString());
        }

        private static int SkipBracket(string s, int i, int e, char open, char close)
        {
            int depth = 0;
            for (; i < e; i++)
            {
                if (s[i] == open)
                    depth++;
                else if (s[i] == close && --depth == 0)
                    return i;
            }
            return e - 1;
        }

        private static string TrimGeneratedSuffixes(string m)
        {
            if (m.IndexOf('<') < 0)
                return m;
            var b = new StringBuilder(m.Length);
            int start = 0;
            for (int i = 0; i <= m.Length; i++)
            {
                if (i < m.Length && m[i] != '.' && m[i] != '+')
                    continue;
                string seg = m.Substring(start, i - start);
                if (seg.StartsWith("<", StringComparison.Ordinal))
                    seg = seg.TrimEnd('0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '_', '|');
                b.Append(seg);
                if (i < m.Length)
                    b.Append(m[i]);
                start = i + 1;
            }
            return b.ToString();
        }

        internal static string ShortType(string t)
        {
            int i = t.IndexOfAny(new[] { '`', '[' });
            if (i >= 0)
                t = t.Substring(0, i);
            i = t.LastIndexOfAny(new[] { '.', '+' });
            if (i >= 0)
                t = t.Substring(i + 1);
            return t;
        }

        /// <summary>Message template: the first line with ids, numbers, GUIDs, hex and quoted values replaced.</summary>
        internal static string Template(string msg)
        {
            string s = TruncUtf16(FirstLine(msg ?? ""), TemplateSourceLength);
            var b = new StringBuilder(s.Length);
            int i = 0;
            while (i < s.Length)
            {
                char c = s[i];
                bool prevAlnum = i > 0 && IsAlnum(s[i - 1]);
                if ((c == '\'' || c == '"' || c == '`') && !prevAlnum)
                {
                    int j = ClosingQuote(s, i);
                    if (j > 0)
                    {
                        b.Append(c).Append('*').Append(c);
                        i = j + 1;
                        continue;
                    }
                }
                else if (!prevAlnum && IsGuidAt(s, i))
                {
                    b.Append("<guid>");
                    i += 36;
                    continue;
                }
                else if (!prevAlnum && c == '0' && i + 2 < s.Length && (s[i + 1] == 'x' || s[i + 1] == 'X') && IsHex(s[i + 2]))
                {
                    int j = i + 2;
                    while (j < s.Length && IsHex(s[j]))
                        j++;
                    b.Append("<hex>");
                    i = j;
                    continue;
                }
                if (!prevAlnum && IsAlnum(c))
                {
                    int e = i;
                    while (e < s.Length && IsAlnum(s[e]))
                        e++;
                    if (IsHexToken(s, i, e))
                    {
                        b.Append("<hex>");
                        i = e;
                        continue;
                    }
                }
                if (IsDigit(c) || c == '-' && !prevAlnum && i + 1 < s.Length && IsDigit(s[i + 1]))
                {
                    int j = i + 1;
                    while (j < s.Length && IsDigit(s[j]))
                        j++;
                    while (j + 1 < s.Length && (s[j] == '.' || s[j] == ',') && IsDigit(s[j + 1]))
                    {
                        j++;
                        while (j < s.Length && IsDigit(s[j]))
                            j++;
                    }
                    b.Append("<n>");
                    i = j;
                    continue;
                }
                if (c == '=')
                {
                    b.Append('=');
                    int e = i + 1;
                    if (e < s.Length && " \t'\"`".IndexOf(s[e]) < 0)
                    {
                        while (e < s.Length && " \t,;)]}".IndexOf(s[e]) < 0)
                            e++;
                    }
                    if (e > i + 1)
                        b.Append("<v>");
                    i = e;
                    continue;
                }
                if (c == ' ' || c == '\t')
                {
                    if (b.Length > 0 && b[b.Length - 1] != ' ')
                        b.Append(' ');
                    i++;
                    continue;
                }
                b.Append(c);
                i++;
            }
            int len = b.Length;
            while (len > 0 && b[len - 1] == ' ')
                len--;
            return TruncUtf16(b.ToString(0, len), TemplateLength);
        }

        private static int ClosingQuote(string s, int i)
        {
            char q = s[i];
            for (int j = i + 1; j < s.Length; j++)
            {
                if (s[j] == q && (j + 1 == s.Length || !IsAlnum(s[j + 1])))
                    return j;
            }
            return -1;
        }

        private static bool IsGuidAt(string s, int i)
        {
            if (i + 36 > s.Length)
                return false;
            for (int k = 0; k < 36; k++)
            {
                char c = s[i + k];
                if (k == 8 || k == 13 || k == 18 || k == 23)
                {
                    if (c != '-')
                        return false;
                }
                else if (!IsHex(c))
                {
                    return false;
                }
            }
            return i + 36 == s.Length || !IsAlnum(s[i + 36]);
        }

        private static bool IsHexToken(string s, int start, int end)
        {
            if (end - start < 8)
                return false;
            bool digit = false, letter = false;
            for (int i = start; i < end; i++)
            {
                char c = s[i];
                if (IsDigit(c))
                    digit = true;
                else if (IsHex(c))
                    letter = true;
                else
                    return false;
            }
            return digit && letter;
        }

        private static string TruncUtf16(string s, int n)
        {
            if (s.Length <= n)
                return s;
            if (char.IsHighSurrogate(s[n - 1]))
                n--;
            return s.Substring(0, n);
        }

        /// <summary>FNV-1a 64 of the UTF-8 bytes of s, as 16 lowercase hex digits (as Go).</summary>
        internal static string Hash(string s)
        {
            ulong h = 14695981039346656037UL;
            for (int i = 0; i < s.Length; i++)
            {
                int cp = s[i];
                if (char.IsHighSurrogate(s[i]) && i + 1 < s.Length && char.IsLowSurrogate(s[i + 1]))
                {
                    cp = char.ConvertToUtf32(s[i], s[i + 1]);
                    i++;
                }
                else if (char.IsSurrogate(s[i]))
                {
                    cp = 0xFFFD; // as Go's JSON decoding stores a lone surrogate
                }
                if (cp < 0x80)
                {
                    h = Fnv(h, cp);
                }
                else if (cp < 0x800)
                {
                    h = Fnv(h, 0xC0 | (cp >> 6));
                    h = Fnv(h, 0x80 | (cp & 0x3F));
                }
                else if (cp < 0x10000)
                {
                    h = Fnv(h, 0xE0 | (cp >> 12));
                    h = Fnv(h, 0x80 | ((cp >> 6) & 0x3F));
                    h = Fnv(h, 0x80 | (cp & 0x3F));
                }
                else
                {
                    h = Fnv(h, 0xF0 | (cp >> 18));
                    h = Fnv(h, 0x80 | ((cp >> 12) & 0x3F));
                    h = Fnv(h, 0x80 | ((cp >> 6) & 0x3F));
                    h = Fnv(h, 0x80 | (cp & 0x3F));
                }
            }
            return h.ToString("x16", CultureInfo.InvariantCulture);
        }

        private static ulong Fnv(ulong h, int b)
        {
            unchecked
            {
                return (h ^ (byte)b) * 1099511628211UL;
            }
        }

        // ---- compaction of an embedded exception (SDK only) ----

        private const int MaxCompactLineLength = 512;
        private const int MaxCompactHeaderChars = 2000;

        /// <summary>
        /// Fits an embedded exception into maxLength characters, keeping what
        /// the fingerprint and the detail page need: the header (types and
        /// messages, at most 2000 characters), every marker line, and the first
        /// frames of each exception (32, then fewer until it fits); dropped
        /// frames are replaced by "  ... (N frames omitted)". The server parses
        /// what it receives, so the fingerprint is computed on the result.
        /// </summary>
        internal static string Compact(string text, int maxLength)
        {
            if (text.Length <= maxLength)
                return text;
            // Classify every line once: 0 other, 1 marker, 2 frame.
            var lines = SplitLines(text);
            var kinds = new byte[lines.Length];
            for (int i = 0; i < lines.Length; i++)
            {
                if (lines[i].Length > MaxCompactLineLength)
                    lines[i] = TruncUtf16(lines[i], MaxCompactLineLength);
                kinds[i] = IsMarker(lines[i]) ? (byte)1 : ParseFrame(lines[i]) != null ? (byte)2 : (byte)0;
            }
            int keepFrames = 1;
            foreach (int keep in new[] { 32, 16, 8, 4, 2 })
            {
                if (CompactWith(null, lines, kinds, keep) <= maxLength)
                {
                    keepFrames = keep;
                    break;
                }
            }
            var sb = new StringBuilder(maxLength + 64);
            CompactWith(sb, lines, kinds, keepFrames);
            if (sb.Length <= maxLength)
                return sb.ToString();
            string result = sb.ToString();
            int cut = result.LastIndexOf('\n', maxLength - 1);
            if (cut <= 0)
                cut = maxLength;
            if (char.IsHighSurrogate(result[cut - 1]))
                cut--;
            return result.Substring(0, cut);
        }

        // Writes the compacted text to b, or with b null only measures it;
        // returns its length.
        private static int CompactWith(StringBuilder b, string[] lines, byte[] kinds, int keepFrames)
        {
            int length = -1; // the first line has no '\n' before it
            int frames = 0, omitted = 0, otherChars = 0;
            bool otherCut = false;
            for (int i = 0; i < lines.Length; i++)
            {
                string l = lines[i];
                if (kinds[i] != 2 && omitted > 0)
                {
                    length += Append(b, length < 0, "  ... (" + omitted.ToString(CultureInfo.InvariantCulture) + " frames omitted)");
                    omitted = 0;
                }
                if (kinds[i] == 1)
                {
                    frames = 0;
                    length += Append(b, length < 0, l);
                }
                else if (kinds[i] == 2)
                {
                    if (frames++ < keepFrames)
                        length += Append(b, length < 0, l);
                    else
                        omitted++;
                }
                else if (otherChars + l.Length <= MaxCompactHeaderChars)
                {
                    otherChars += l.Length;
                    length += Append(b, length < 0, l);
                }
                else if (!otherCut)
                {
                    otherCut = true;
                    length += Append(b, length < 0, "  ... (message cut)");
                }
            }
            if (omitted > 0)
                length += Append(b, length < 0, "  ... (" + omitted.ToString(CultureInfo.InvariantCulture) + " frames omitted)");
            return Math.Max(length, 0);
        }

        private static int Append(StringBuilder b, bool first, string line)
        {
            if (b != null)
            {
                if (!first)
                    b.Append('\n');
                b.Append(line);
            }
            return line.Length + 1;
        }

        private static bool IsLetter(char c) => c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z';
        private static bool IsDigit(char c) => c >= '0' && c <= '9';
        private static bool IsAlnum(char c) => IsLetter(c) || IsDigit(c);
        private static bool IsHex(char c) => IsDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F';
    }
}
