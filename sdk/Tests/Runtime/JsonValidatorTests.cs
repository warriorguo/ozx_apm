using System.Text;
using NUnit.Framework;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    [TestFixture]
    public class JsonValidatorTests
    {
        private static bool Valid(string s)
        {
            var b = Encoding.UTF8.GetBytes(s);
            return JsonValidator.IsObject(b, 0, b.Length);
        }

        [Test]
        public void AcceptsWellFormedObjects()
        {
            Assert.IsTrue(Valid("{}"));
            Assert.IsTrue(Valid("{\"a\":1,\"b\":-2.5e-3,\"c\":[true,false,null,\"x\\\"y\\u00e9\"],\"d\":{\"e\":[]}}"));
            Assert.IsTrue(Valid(" {\"emoji\":\"🎮 中文\"} "));
        }

        [Test]
        public void RejectsDamagedLines()
        {
            Assert.IsFalse(Valid("{\"type\":\"perf_sample\",\"se"));                       // cut off
            Assert.IsFalse(Valid("{\"a\":\"torn\",\"se{\"type\":\"x\",\"fps\":2.00}"));      // cut off + next line
            Assert.IsFalse(Valid("{\"fps\":59,50}"));                                       // old de-DE output
            Assert.IsFalse(Valid("{\"a\":1}{\"b\":2}"));                                    // two objects
            Assert.IsFalse(Valid("{\"a\":1,}"));
            Assert.IsFalse(Valid("[1,2]"));
            Assert.IsFalse(Valid("{\"a\":\"line\nbreak\"}"));
            Assert.IsFalse(Valid("\0\0\0{\"a\":1}"));
            Assert.IsFalse(Valid(""));
        }

        [Test]
        public void AcceptsEverythingTheSerializerWrites()
        {
            var evt = new CrashEvent
            {
                event_id = "id", seq = 5, timestamp = 1, crash_type = "native", fingerprint = "f",
                stack = "at A\n\tat B \"quoted\" \\ \u0001",
                breadcrumbs = { "a", "b}{", "" }
            };
            Assert.IsTrue(Valid(JsonSerializer.SerializeEvent(evt)));
            Assert.IsTrue(Valid(JsonSerializer.SerializeEvent(new PerfSampleEvent { fps = float.NaN })));
        }
    }
}
