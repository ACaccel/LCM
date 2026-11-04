package configsync

import (
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
)

// canonicalYAML renders the canonical JSON form of a document as the block
// style YAML that export writes, so a diff of two versions shows only real
// changes.
func canonicalYAML(canon []byte) ([]byte, error) {
	doc, err := model.Decode(canon)
	if err != nil {
		return nil, fmt.Errorf("configsync: render yaml: %w", err)
	}
	return model.EncodeYAML(doc)
}
