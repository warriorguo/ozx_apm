using System;

namespace OzxApm.Models
{
    [Serializable]
    public class CommonContext
    {
        public long timestamp;
        public string app_version;
        public string build;
        public string unity_version;
        public string platform;
        public string os_version;
        public string device_model;
        public string cpu;
        public string gpu;
        public string ram_class;
        public string session_id;
        public string device_id;
        public string user_id;
        public string scene;
        public string level_id;
        public string net_type;
        public string country;
    }
}
