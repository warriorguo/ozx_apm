using System;
using System.Threading;

namespace OzxApm.Utils
{
    /// <summary>
    /// Lock-free ring buffer for high-performance event queueing
    /// </summary>
    public class RingBuffer<T> where T : class
    {
        private readonly T[] _buffer;
        private readonly int _capacity;
        private int _head;
        private int _tail;
        private int _count;

        public int Count => _count;
        public int Capacity => _capacity;
        public bool IsFull => _count >= _capacity;
        public bool IsEmpty => _count == 0;

        public RingBuffer(int capacity)
        {
            if (capacity <= 0)
                throw new ArgumentException("Capacity must be positive", nameof(capacity));

            _capacity = capacity;
            _buffer = new T[capacity];
            _head = 0;
            _tail = 0;
            _count = 0;
        }

        /// <summary>
        /// Attempts to add an item. Returns false if full.
        /// </summary>
        public bool TryEnqueue(T item)
        {
            if (item == null)
                return false;

            int currentCount = Interlocked.CompareExchange(ref _count, 0, 0);
            if (currentCount >= _capacity)
                return false;

            int newTail = Interlocked.Increment(ref _tail) - 1;
            int index = newTail % _capacity;

            _buffer[index] = item;
            Interlocked.Increment(ref _count);

            return true;
        }

        /// <summary>
        /// Adds an item, overwriting oldest if full.
        /// </summary>
        public void EnqueueOverwrite(T item)
        {
            if (item == null)
                return;

            int newTail = Interlocked.Increment(ref _tail) - 1;
            int index = newTail % _capacity;

            _buffer[index] = item;

            int currentCount = Interlocked.Increment(ref _count);
            if (currentCount > _capacity)
            {
                Interlocked.Exchange(ref _count, _capacity);
                Interlocked.Increment(ref _head);
            }
        }

        /// <summary>
        /// Attempts to remove and return an item. Returns false if empty.
        /// </summary>
        public bool TryDequeue(out T item)
        {
            item = null;

            int currentCount = Interlocked.CompareExchange(ref _count, 0, 0);
            if (currentCount <= 0)
                return false;

            int newHead = Interlocked.Increment(ref _head) - 1;
            int index = newHead % _capacity;

            item = _buffer[index];
            _buffer[index] = null;
            Interlocked.Decrement(ref _count);

            return item != null;
        }

        /// <summary>
        /// Dequeues up to maxCount items into the provided array.
        /// Returns the number of items dequeued.
        /// </summary>
        public int DequeueBatch(T[] destination, int maxCount)
        {
            if (destination == null || maxCount <= 0)
                return 0;

            int dequeued = 0;
            while (dequeued < maxCount && dequeued < destination.Length)
            {
                if (TryDequeue(out T item))
                {
                    destination[dequeued] = item;
                    dequeued++;
                }
                else
                {
                    break;
                }
            }

            return dequeued;
        }

        /// <summary>
        /// Peeks at the next item without removing it.
        /// </summary>
        public bool TryPeek(out T item)
        {
            item = null;

            if (_count <= 0)
                return false;

            int index = _head % _capacity;
            item = _buffer[index];

            return item != null;
        }

        /// <summary>
        /// Clears all items from the buffer.
        /// </summary>
        public void Clear()
        {
            while (TryDequeue(out _)) { }
            _head = 0;
            _tail = 0;
            _count = 0;
        }
    }
}
