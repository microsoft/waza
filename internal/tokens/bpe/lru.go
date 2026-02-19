package bpe

type lruNode struct {
	key   string
	value []int
	next  *lruNode
	prev  *lruNode
}

// LRUCache is a simple O(1) LRU cache keyed by tokenized text fragments.
type LRUCache struct {
	size  int
	nodes map[string]*lruNode
	head  *lruNode
	tail  *lruNode
}

func NewLRUCache(size int) *LRUCache {
	return &LRUCache{
		size:  size,
		nodes: map[string]*lruNode{},
	}
}

func (c *LRUCache) Get(key string) ([]int, bool) {
	node, ok := c.nodes[key]
	if !ok {
		return nil, false
	}
	c.moveToHead(node)
	return node.value, true
}

func (c *LRUCache) Set(key string, value []int) {
	if node, ok := c.nodes[key]; ok {
		node.value = value
		c.moveToHead(node)
		return
	}

	node := &lruNode{
		key:   key,
		value: value,
	}
	c.nodes[key] = node
	c.addNode(node)
	if len(c.nodes) > c.size {
		delete(c.nodes, c.tail.key)
		c.removeNode(c.tail)
	}
}

func (c *LRUCache) moveToHead(node *lruNode) {
	c.removeNode(node)
	node.prev = nil
	node.next = nil
	c.addNode(node)
}

func (c *LRUCache) addNode(node *lruNode) {
	if c.head != nil {
		c.head.prev = node
		node.next = c.head
	}
	if c.tail == nil {
		c.tail = node
	}
	c.head = node
}

func (c *LRUCache) removeNode(node *lruNode) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		c.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		c.tail = node.prev
	}
}
