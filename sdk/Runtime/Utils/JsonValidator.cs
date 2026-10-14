namespace OzxApm.Utils
{
    /// <summary>
    /// Checks that a byte range is exactly one well-formed JSON object.
    /// Used before upload to drop stored lines that were damaged by a crash or a
    /// failed write (for example a cut-off line that later data was appended to),
    /// so one bad line cannot make the whole batch invalid. No allocations.
    /// </summary>
    internal static class JsonValidator
    {
        private const int MaxDepth = 64;

        public static bool IsObject(byte[] data, int start, int end)
        {
            if (data == null || start < 0 || end > data.Length || start >= end)
                return false;

            int i = SkipSpace(data, start, end);
            if (i >= end || data[i] != (byte)'{')
                return false;
            if (!Value(data, ref i, end, 0))
                return false;
            return SkipSpace(data, i, end) == end;
        }

        private static bool Value(byte[] d, ref int i, int end, int depth)
        {
            i = SkipSpace(d, i, end);
            if (i >= end || depth > MaxDepth)
                return false;

            switch (d[i])
            {
                case (byte)'{': return Object(d, ref i, end, depth + 1);
                case (byte)'[': return Array(d, ref i, end, depth + 1);
                case (byte)'"': return String(d, ref i, end);
                case (byte)'t': return Literal(d, ref i, end, "true");
                case (byte)'f': return Literal(d, ref i, end, "false");
                case (byte)'n': return Literal(d, ref i, end, "null");
                default: return Number(d, ref i, end);
            }
        }

        private static bool Object(byte[] d, ref int i, int end, int depth)
        {
            i++; // {
            i = SkipSpace(d, i, end);
            if (i < end && d[i] == (byte)'}')
            {
                i++;
                return true;
            }
            while (true)
            {
                i = SkipSpace(d, i, end);
                if (i >= end || d[i] != (byte)'"' || !String(d, ref i, end))
                    return false;
                i = SkipSpace(d, i, end);
                if (i >= end || d[i] != (byte)':')
                    return false;
                i++;
                if (!Value(d, ref i, end, depth))
                    return false;
                i = SkipSpace(d, i, end);
                if (i >= end)
                    return false;
                if (d[i] == (byte)',')
                {
                    i++;
                    continue;
                }
                if (d[i] == (byte)'}')
                {
                    i++;
                    return true;
                }
                return false;
            }
        }

        private static bool Array(byte[] d, ref int i, int end, int depth)
        {
            i++; // [
            i = SkipSpace(d, i, end);
            if (i < end && d[i] == (byte)']')
            {
                i++;
                return true;
            }
            while (true)
            {
                if (!Value(d, ref i, end, depth))
                    return false;
                i = SkipSpace(d, i, end);
                if (i >= end)
                    return false;
                if (d[i] == (byte)',')
                {
                    i++;
                    continue;
                }
                if (d[i] == (byte)']')
                {
                    i++;
                    return true;
                }
                return false;
            }
        }

        private static bool String(byte[] d, ref int i, int end)
        {
            i++; // opening quote
            while (i < end)
            {
                byte c = d[i];
                if (c == (byte)'"')
                {
                    i++;
                    return true;
                }
                if (c < 0x20)
                    return false;
                if (c == (byte)'\\')
                {
                    i++;
                    if (i >= end)
                        return false;
                    switch (d[i])
                    {
                        case (byte)'"':
                        case (byte)'\\':
                        case (byte)'/':
                        case (byte)'b':
                        case (byte)'f':
                        case (byte)'n':
                        case (byte)'r':
                        case (byte)'t':
                            break;
                        case (byte)'u':
                            if (i + 4 >= end)
                                return false;
                            for (int k = 1; k <= 4; k++)
                            {
                                if (!IsHex(d[i + k]))
                                    return false;
                            }
                            i += 4;
                            break;
                        default:
                            return false;
                    }
                }
                i++;
            }
            return false;
        }

        private static bool Number(byte[] d, ref int i, int end)
        {
            int start = i;
            if (i < end && d[i] == (byte)'-')
                i++;
            if (!Digits(d, ref i, end))
                return false;
            if (i < end && d[i] == (byte)'.')
            {
                i++;
                if (!Digits(d, ref i, end))
                    return false;
            }
            if (i < end && (d[i] == (byte)'e' || d[i] == (byte)'E'))
            {
                i++;
                if (i < end && (d[i] == (byte)'+' || d[i] == (byte)'-'))
                    i++;
                if (!Digits(d, ref i, end))
                    return false;
            }
            return i > start;
        }

        private static bool Digits(byte[] d, ref int i, int end)
        {
            int start = i;
            while (i < end && d[i] >= (byte)'0' && d[i] <= (byte)'9')
                i++;
            return i > start;
        }

        private static bool Literal(byte[] d, ref int i, int end, string word)
        {
            if (i + word.Length > end)
                return false;
            for (int k = 0; k < word.Length; k++)
            {
                if (d[i + k] != (byte)word[k])
                    return false;
            }
            i += word.Length;
            return true;
        }

        private static bool IsHex(byte c)
        {
            return (c >= (byte)'0' && c <= (byte)'9') || (c >= (byte)'a' && c <= (byte)'f') || (c >= (byte)'A' && c <= (byte)'F');
        }

        private static int SkipSpace(byte[] d, int i, int end)
        {
            while (i < end && (d[i] == (byte)' ' || d[i] == (byte)'\t' || d[i] == (byte)'\r' || d[i] == (byte)'\n'))
                i++;
            return i;
        }
    }
}
