using NUnit.Framework;
using OzxApm.Utils;

namespace OzxApm.Tests
{
    [TestFixture]
    public class RingBufferTests
    {
        [Test]
        public void Constructor_CreatesEmptyBuffer()
        {
            var buffer = new RingBuffer<string>(10);
            Assert.AreEqual(0, buffer.Count);
            Assert.AreEqual(10, buffer.Capacity);
            Assert.IsTrue(buffer.IsEmpty);
            Assert.IsFalse(buffer.IsFull);
        }

        [Test]
        public void TryEnqueue_AddsItem()
        {
            var buffer = new RingBuffer<string>(10);
            Assert.IsTrue(buffer.TryEnqueue("test"));
            Assert.AreEqual(1, buffer.Count);
        }

        [Test]
        public void TryEnqueue_RejectsFull()
        {
            var buffer = new RingBuffer<string>(2);
            Assert.IsTrue(buffer.TryEnqueue("a"));
            Assert.IsTrue(buffer.TryEnqueue("b"));
            Assert.IsFalse(buffer.TryEnqueue("c"));
            Assert.AreEqual(2, buffer.Count);
        }

        [Test]
        public void EnqueueOverwrite_OverwritesOldest()
        {
            var buffer = new RingBuffer<string>(2);
            buffer.EnqueueOverwrite("a");
            buffer.EnqueueOverwrite("b");
            buffer.EnqueueOverwrite("c");

            Assert.AreEqual(2, buffer.Count);

            // Oldest item should be overwritten
            Assert.IsTrue(buffer.TryDequeue(out var item1));
            Assert.IsTrue(buffer.TryDequeue(out var item2));

            // Should get b and c (a was overwritten)
            Assert.IsTrue(item1 == "b" || item1 == "c");
        }

        [Test]
        public void TryDequeue_RemovesItem()
        {
            var buffer = new RingBuffer<string>(10);
            buffer.TryEnqueue("test");

            Assert.IsTrue(buffer.TryDequeue(out var item));
            Assert.AreEqual("test", item);
            Assert.AreEqual(0, buffer.Count);
        }

        [Test]
        public void TryDequeue_ReturnsFalseWhenEmpty()
        {
            var buffer = new RingBuffer<string>(10);
            Assert.IsFalse(buffer.TryDequeue(out var item));
            Assert.IsNull(item);
        }

        [Test]
        public void DequeueBatch_DequeuesMultiple()
        {
            var buffer = new RingBuffer<string>(10);
            buffer.TryEnqueue("a");
            buffer.TryEnqueue("b");
            buffer.TryEnqueue("c");

            var result = new string[5];
            int count = buffer.DequeueBatch(result, 2);

            Assert.AreEqual(2, count);
            Assert.AreEqual("a", result[0]);
            Assert.AreEqual("b", result[1]);
            Assert.AreEqual(1, buffer.Count);
        }

        [Test]
        public void TryPeek_DoesNotRemove()
        {
            var buffer = new RingBuffer<string>(10);
            buffer.TryEnqueue("test");

            Assert.IsTrue(buffer.TryPeek(out var item));
            Assert.AreEqual("test", item);
            Assert.AreEqual(1, buffer.Count);
        }

        [Test]
        public void Clear_RemovesAllItems()
        {
            var buffer = new RingBuffer<string>(10);
            buffer.TryEnqueue("a");
            buffer.TryEnqueue("b");
            buffer.Clear();

            Assert.AreEqual(0, buffer.Count);
            Assert.IsTrue(buffer.IsEmpty);
        }

        [Test]
        public void FIFO_OrderPreserved()
        {
            var buffer = new RingBuffer<string>(10);
            buffer.TryEnqueue("first");
            buffer.TryEnqueue("second");
            buffer.TryEnqueue("third");

            Assert.IsTrue(buffer.TryDequeue(out var item1));
            Assert.AreEqual("first", item1);

            Assert.IsTrue(buffer.TryDequeue(out var item2));
            Assert.AreEqual("second", item2);

            Assert.IsTrue(buffer.TryDequeue(out var item3));
            Assert.AreEqual("third", item3);
        }
    }
}
