using System.IO;
using UnityEditor.Build;
using UnityEditor.Build.Reporting;
using UnityEditor.UnityLinker;

namespace OzxApm.Editor
{
    /// <summary>
    /// Gives the linker the SDK's link.xml (Runtime/link.xml) when a player is
    /// built with managed code stripping (APM-7): Unity does not collect
    /// link.xml files from packages on every version. The content is written
    /// to the project's Temp folder and that file is returned, so it works
    /// wherever the package lives (registry cache, embedded, local path).
    /// </summary>
    internal sealed class LinkXmlProvider : IUnityLinkerProcessor
    {
        // Same content as Runtime/link.xml (LinkXmlTests compares them). TextAsset:
        // the engine's MonoScript class (ID 115), needed by AddComponent<ApmClient>,
        // stays under "Strip Engine Code" only while the managed TextAsset type does.
        internal const string LinkXml =
            "<linker>\n" +
            "  <assembly fullname=\"OzxApm\" preserve=\"all\"/>\n" +
            "  <assembly fullname=\"UnityEngine.CoreModule\">\n" +
            "    <type fullname=\"UnityEngine.TextAsset\" preserve=\"all\"/>\n" +
            "  </assembly>\n" +
            "</linker>\n";

        public int callbackOrder => 0;

        public string GenerateAdditionalLinkXmlFile(BuildReport report, UnityLinkerBuildPipelineData data)
        {
            string dir = Path.GetFullPath(Path.Combine("Temp", "OzxApm"));
            Directory.CreateDirectory(dir);
            string path = Path.Combine(dir, "link.xml");
            File.WriteAllText(path, LinkXml);
            return path;
        }

#if !UNITY_2021_2_OR_NEWER
        public void OnBeforeRun(BuildReport report, UnityLinkerBuildPipelineData data)
        {
        }

        public void OnAfterRun(BuildReport report, UnityLinkerBuildPipelineData data)
        {
        }
#endif
    }
}
