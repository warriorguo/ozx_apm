using NUnit.Framework;
using System.Collections.Generic;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Tests
{
    [TestFixture]
    public class EventQueueTests
    {
        private ApmConfig _config;
        private EventQueue _queue;
        private List<List<BaseEvent>> _flushedBatches;

        [SetUp]
        public void SetUp()
        {
            _config = new ApmConfig
            {
                BatchSize = 5,
                MaxQueueSize = 100,
                FlushIntervalSeconds = 60f // Long interval so we control flushing
            };
            _queue = new EventQueue(_config);
            _flushedBatches = new List<List<BaseEvent>>();

            _queue.OnBatchReady += batch => _flushedBatches.Add(new List<BaseEvent>(batch));
        }

        [Test]
        public void Constructor_CreatesEmptyQueue()
        {
            Assert.AreEqual(0, _queue.Count);
            Assert.IsFalse(_queue.IsFull);
        }

        [Test]
        public void Enqueue_AddsEvent()
        {
            var evt = new PerfSampleEvent { fps = 60 };
            _queue.Enqueue(evt);

            Assert.AreEqual(1, _queue.Count);
        }

        [Test]
        public void Enqueue_FlushesWhenBatchSizeReached()
        {
            for (int i = 0; i < 5; i++)
            {
                _queue.Enqueue(new PerfSampleEvent { fps = 60 + i });
            }

            Assert.AreEqual(1, _flushedBatches.Count);
            Assert.AreEqual(5, _flushedBatches[0].Count);
            Assert.AreEqual(0, _queue.Count);
        }

        [Test]
        public void Flush_SendsAllEvents()
        {
            _queue.Enqueue(new PerfSampleEvent { fps = 60 });
            _queue.Enqueue(new PerfSampleEvent { fps = 55 });

            _queue.Flush();

            Assert.AreEqual(1, _flushedBatches.Count);
            Assert.AreEqual(2, _flushedBatches[0].Count);
        }

        [Test]
        public void Clear_RemovesAllEvents()
        {
            _queue.Enqueue(new PerfSampleEvent { fps = 60 });
            _queue.Enqueue(new PerfSampleEvent { fps = 55 });
            _queue.Clear();

            Assert.AreEqual(0, _queue.Count);

            _queue.Flush();
            Assert.AreEqual(0, _flushedBatches.Count);
        }

        [Test]
        public void Enqueue_RejectsNull()
        {
            _queue.Enqueue(null);
            Assert.AreEqual(0, _queue.Count);
        }

        [Test]
        public void MultipleBatches_FlushCorrectly()
        {
            // Enqueue 12 events with batch size of 5
            for (int i = 0; i < 12; i++)
            {
                _queue.Enqueue(new PerfSampleEvent { fps = 60 + i });
            }

            // Should have flushed 2 complete batches
            Assert.AreEqual(2, _flushedBatches.Count);
            Assert.AreEqual(5, _flushedBatches[0].Count);
            Assert.AreEqual(5, _flushedBatches[1].Count);

            // 2 remaining
            Assert.AreEqual(2, _queue.Count);
        }
    }
}
