package cryptography

import "github.com/ethereum/go-ethereum/rlp"

// EncodeRLP delegates Ethereum Recursive Length Prefix encoding to go-ethereum.
// Use []byte for bytes, string for UTF-8 text, unsigned Go integers for numeric
// values and []any for nested lists. There is no JSON or hash conversion.
func EncodeRLP(value any) ([]byte, error) { return rlp.EncodeToBytes(value) }

// DecodeRLP requires exactly one complete canonical RLP value. Byte strings
// become []byte and lists become []any, preserving all external bytes.
func DecodeRLP(data []byte) (any, error) {
	var value any
	if err := rlp.DecodeBytes(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// DecodeRLPInto exposes SDK typed decoding, including canonical integer checks.
func DecodeRLPInto(data []byte, destination any) error { return rlp.DecodeBytes(data, destination) }
