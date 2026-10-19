using System.IO;
using NUnit.Framework;
using OzxApm.Editor;

namespace OzxApm.Tests
{
    /// <summary>APM-7: the SDK is kept whole under code stripping.</summary>
    [TestFixture]
    public class LinkXmlTests
    {
        private const string Preserve = "<assembly fullname=\"OzxApm\" preserve=\"all\"/>";

        [Test]
        public void Provider_WritesTheLinkXmlTheLinkerGets()
        {
            string path = new LinkXmlProvider().GenerateAdditionalLinkXmlFile(null, null);
            Assert.IsTrue(File.Exists(path), path);
            string xml = File.ReadAllText(path);
            StringAssert.Contains(Preserve, xml);
            StringAssert.StartsWith("<linker>", xml);
        }

        [Test]
        public void PackageLinkXml_PreservesTheSdkAssembly()
        {
            // The package's own copy (for versions that read link.xml in packages).
            string path = Path.GetFullPath(Path.Combine("Packages", "com.ozx.apm", "Runtime", "link.xml"));
            if (!File.Exists(path))
                Assert.Ignore("the package is not at Packages/com.ozx.apm in this project");
            StringAssert.Contains(Preserve, File.ReadAllText(path));
        }
    }
}
