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
        // MonoScript (class ID 115), needed by AddComponent<ApmClient>, survives
        // "Strip Engine Code" only with the managed TextAsset type.
        private const string PreserveTextAsset = "<type fullname=\"UnityEngine.TextAsset\" preserve=\"all\"/>";

        [Test]
        public void Provider_WritesTheLinkXmlTheLinkerGets()
        {
            string path = new LinkXmlProvider().GenerateAdditionalLinkXmlFile(null, null);
            Assert.IsTrue(File.Exists(path), path);
            string xml = File.ReadAllText(path);
            StringAssert.Contains(Preserve, xml);
            StringAssert.Contains(PreserveTextAsset, xml);
            StringAssert.Contains("<assembly fullname=\"UnityEngine.CoreModule\">", xml);
            StringAssert.StartsWith("<linker>", xml);
            // Well-formed, and the same rules as the package's link.xml.
            var doc = new System.Xml.XmlDocument();
            doc.LoadXml(xml);
            Assert.AreEqual(2, doc.SelectNodes("/linker/assembly").Count);
        }

        [Test]
        public void ProviderAndPackageLinkXml_HaveTheSameRules()
        {
            string package = FindPackageLinkXml();
            if (package == null)
                Assert.Ignore("the package's Runtime/link.xml is not found from this project");
            var a = new System.Xml.XmlDocument();
            a.LoadXml(LinkXmlProvider.LinkXml);
            var b = new System.Xml.XmlDocument();
            b.Load(package);
            Assert.AreEqual(a.SelectSingleNode("/linker").OuterXml.Replace(" ", ""), Strip(b).Replace(" ", ""));
        }

        // The package's link.xml without its comment.
        private static string Strip(System.Xml.XmlDocument doc)
        {
            var linker = doc.SelectSingleNode("/linker");
            foreach (System.Xml.XmlNode c in doc.SelectNodes("//comment()"))
                c.ParentNode.RemoveChild(c);
            return linker.OuterXml;
        }

        private static string FindPackageLinkXml()
        {
            foreach (var p in new[]
            {
                Path.Combine("Packages", "com.ozx.apm", "Runtime", "link.xml"),
                Path.GetFullPath(Path.Combine("Packages", "com.ozx.apm", "Runtime", "link.xml")),
            })
            {
                if (File.Exists(p))
                    return p;
            }
            return null;
        }

        [Test]
        public void PackageLinkXml_PreservesTheSdkAssembly()
        {
            // The package's own copy (for versions that read link.xml in packages).
            string path = Path.GetFullPath(Path.Combine("Packages", "com.ozx.apm", "Runtime", "link.xml"));
            if (!File.Exists(path))
                Assert.Ignore("the package is not at Packages/com.ozx.apm in this project");
            StringAssert.Contains(Preserve, File.ReadAllText(path));
            StringAssert.Contains(PreserveTextAsset, File.ReadAllText(path));
        }
    }
}
