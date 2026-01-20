namespace OzxApm.Collectors
{
    /// <summary>
    /// Interface for all data collectors
    /// </summary>
    public interface ICollector
    {
        /// <summary>
        /// Starts the collector
        /// </summary>
        void Start();

        /// <summary>
        /// Updates the collector (called every frame)
        /// </summary>
        void Update();

        /// <summary>
        /// Stops the collector
        /// </summary>
        void Stop();

        /// <summary>
        /// Whether the collector is currently active
        /// </summary>
        bool IsActive { get; }
    }
}
