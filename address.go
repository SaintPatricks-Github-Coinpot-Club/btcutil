// Copyright (c) 2013-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package btcutil

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/binance-chain/btcutil/base58"
	"github.com/binance-chain/btcutil/bech32"
	"github.com/binance-chain/btcutil/chaincfg"
	"github.com/martinboehm/btcd/btcec"
	"golang.org/x/crypto/ripemd160"
)

// UnsupportedWitnessVerError describes an error where a segwit address being
// decoded has an unsupported witness version.
type UnsupportedWitnessVerError byte

func (e UnsupportedWitnessVerError) Error() string {
	return "unsupported witness version: " + strconv.Itoa(int(e))
}

// UnsupportedWitnessProgLenError describes an error where a segwit address
// being decoded has an unsupported witness program length.
type UnsupportedWitnessProgLenError int

func (e UnsupportedWitnessProgLenError) Error() string {
	return "unsupported witness program length: " + strconv.Itoa(int(e))
}

var (
	// ErrChecksumMismatch describes an error where decoding failed due
	// to a bad checksum.
	ErrChecksumMismatch = errors.New("checksum mismatch")

	// ErrUnknownAddressType describes an error where an address can not
	// decoded as a specific address type due to the string encoding
	// begining with an identifier byte unknown to any standard or
	// registered (via chaincfg.Register) network.
	ErrUnknownAddressType = errors.New("unknown address type")

	// ErrAddressCollision describes an error where an address can not
	// be uniquely determined as either a pay-to-pubkey-hash or
	// pay-to-script-hash address since the leading identifier is used for
	// describing both address kinds, but for different networks.  Rather
	// than assuming or defaulting to one or the other, this error is
	// returned and the caller must decide how to decode the address.
	ErrAddressCollision = errors.New("address collision")
)

// encodeAddress returns a human-readable payment address given a ripemd160 hash
// and netID which encodes the bitcoin network and address type.  It is used
// in both pay-to-pubkey-hash (P2PKH) and pay-to-script-hash (P2SH) address
// encoding.
func encodeAddress(hash160, netID []byte, hash base58.CksumHasher) string {
	// Format is 1 byte for a network and address class (i.e. P2PKH vs
	// P2SH), 20 bytes for a RIPEMD160 hash, and 4 bytes of checksum.
	return base58.CheckEncode(hash160[:ripemd160.Size], netID, hash)
}

// encodeSegWitAddress creates a bech32 encoded address string representation
// from witness version and witness program.
func encodeSegWitAddress(hrp string, witnessVersion byte, witnessProgram []byte) (string, error) {
	// Group the address bytes into 5 bit groups, as this is what is used to
	// encode each character in the address string.
	converted, err := bech32.ConvertBits(witnessProgram, 8, 5, true)
	if err != nil {
		return "", err
	}

	// Concatenate the witness version and program, and encode the resulting
	// bytes using bech32 encoding.
	combined := make([]byte, len(converted)+1)
	combined[0] = witnessVersion
	copy(combined[1:], converted)
	var bech string
	if witnessVersion == 0 {
		bech, err = bech32.Encode(hrp, combined)
	} else {
		bech, err = bech32.EncodeM(hrp, combined)
	}
	if err != nil {
		return "", err
	}

	// // Check validity by decoding the created address.
	// version, program, err := decodeSegWitAddress(bech)
	// if err != nil {
	// 	return "", fmt.Errorf("invalid segwit address: %v", err)
	// }

	// if version != witnessVersion || !bytes.Equal(program, witnessProgram) {
	// 	return "", fmt.Errorf("invalid segwit address")
	// }

	return bech, nil
}

// Address is an interface type for any type of destination a transaction
// output may spend to.  This includes pay-to-pubkey (P2PK), pay-to-pubkey-hash
// (P2PKH), and pay-to-script-hash (P2SH).  Address is designed to be generic
// enough that other kinds of addresses may be added in the future without
// changing the decoding and encoding API.
type Address interface {
	// String returns the string encoding of the transaction output
	// destination.
	//
	// Please note that String differs subtly from EncodeAddress: String
	// will return the value as a string without any conversion, while
	// EncodeAddress may convert destination types (for example,
	// converting pubkeys to P2PKH addresses) before encoding as a
	// payment address string.
	String() string

	// EncodeAddress returns the string encoding of the payment address
	// associated with the Address value.  See the comment on String
	// for how this method differs from String.
	EncodeAddress() string

	// ScriptAddress returns the raw bytes of the address to be used
	// when inserting the address into a txout's script.
	ScriptAddress() []byte

	// IsForNet returns whether or not the address is associated with the
	// passed bitcoin network.
	IsForNet(*chaincfg.Params) bool
}

// DecodeAddress decodes the string encoding of an address and returns
// the Address if addr is a valid encoding for a known address type.
//
// The bitcoin network the address is associated with is extracted if possible.
// When the address does not encode the network, such as in the case of a raw
// public key, the address will be associated with the passed defaultNet.
func DecodeAddress(addr string, defaultNet *chaincfg.Params) (Address, error) {
	// Bech32 encoded segwit addresses start with a human-readable part
	// (hrp) followed by '1'. For Bitcoin mainnet the hrp is "bc", and for
	// testnet it is "tb". If the address string has a prefix that matches
	// one of the prefixes for the known networks, we try to decode it as
	// a segwit address.
	oneIndex := strings.LastIndexByte(addr, '1')
	if oneIndex > 1 {
		prefix := addr[:oneIndex+1]
		if chaincfg.IsBech32SegwitPrefix(prefix, defaultNet) {
			witnessVer, witnessProg, err := decodeSegWitAddress(addr)
			// Some non-segwit addresses (for example, Litecoin `L` addresses)
			// may also start with the segwit prefix even though they are
			// legacy addresses. In this case decodeSegWitAddress returns error
			// and we proceed to try decoding as a legacy address below.
			if err == nil {
				if witnessVer > 16 {
					return nil, UnsupportedWitnessVerError(witnessVer)
				}

				// The HRP is everything before the found '1'.
				hrp := prefix[:len(prefix)-1]

				if witnessVer == 0 {
					switch len(witnessProg) {
					case 20:
						return newAddressWitnessPubKeyHash(hrp, witnessProg)
					case 32:
						return newAddressWitnessScriptHash(hrp, witnessProg)
					default:
						return newAddressWitnessUnknown(hrp, witnessVer, witnessProg)
					}
				} else if witnessVer == 1 && len(witnessProg) == 32 {
					return newAddressWitnessTaproot(hrp, witnessProg)
				}
				return newAddressWitnessUnknown(hrp, witnessVer, witnessProg)
			}
		}
	}

	// Serialized public keys are either 65 bytes (130 hex chars) if
	// uncompressed/hybrid or 33 bytes (66 hex chars) if compressed.
	if len(addr) == 130 || len(addr) == 66 {
		serializedPubKey, err := hex.DecodeString(addr)
		if err != nil {
			return nil, err
		}
		return NewAddressPubKey(serializedPubKey, defaultNet)
	}

	// Switch on decoded length to determine the type.
	decoded, netID, err := base58.CheckDecode(addr, defaultNet.AddressMagicLen, defaultNet.Base58CksumHasher)
	if err != nil {
		if err == base58.ErrChecksum {
			return nil, ErrChecksumMismatch
		}
		return nil, errors.New("decoded address is of unknown format")
	}
	switch len(decoded) {
	case ripemd160.Size: // P2PKH or P2SH
		isP2PKH := chaincfg.IsPubKeyHashAddrID(netID, defaultNet)
		isP2SH := chaincfg.IsScriptHashAddrID(netID, defaultNet)
		switch hash160 := decoded; {
		case isP2PKH && isP2SH:
			return nil, ErrAddressCollision
		case isP2PKH:
			return newAddressPubKeyHash(hash160, netID, defaultNet.Base58CksumHasher)
		case isP2SH:
			return newAddressScriptHashFromHash(hash160, netID, defaultNet.Base58CksumHasher)
		default:
			return nil, ErrUnknownAddressType
		}

	default:
		return nil, errors.New("decoded address is of unknown size")
	}
}

// decodeSegWitAddress parses a bech32 encoded segwit address string and
// returns the witness version and witness program byte representation.
func decodeSegWitAddress(address string) (byte, []byte, error) {
	// Decode the bech32 encoded address.
	_, data, err := bech32.Decode(address)
	if err != nil {
		return 0, nil, err
	}

	// The first byte of the decoded address is the witness version, it must
	// exist.
	if len(data) < 1 {
		return 0, nil, fmt.Errorf("no witness version")
	}

	// ...and be <= 16.
	version := data[0]
	if version > 16 {
		return 0, nil, fmt.Errorf("invalid witness version: %v", version)
	}

	// The remaining characters of the address returned are grouped into
	// words of 5 bits. In order to restore the original witness program
	// bytes, we'll need to regroup into 8 bit words.
	regrouped, err := bech32.ConvertBits(data[1:], 5, 8, false)
	if err != nil {
		return 0, nil, err
	}

	// The regrouped data must be between 2 and 40 bytes.
	if len(regrouped) < 2 || len(regrouped) > 40 {
		return 0, nil, fmt.Errorf("invalid data length")
	}

	// For witness version 0, address MUST be exactly 20 or 32 bytes.
	if version == 0 && len(regrouped) != 20 && len(regrouped) != 32 {
		return 0, nil, fmt.Errorf("invalid data length for witness "+
			"version 0: %v", len(regrouped))
	}

	return version, regrouped, nil
}

// AddressPubKeyHash is an Address for a pay-to-pubkey-hash (P2PKH)
// transaction.
type AddressPubKeyHash struct {
	hash        [ripemd160.Size]byte
	netID       []byte
	cksumHasher base58.CksumHasher
}

// NewAddressPubKeyHash returns a new AddressPubKeyHash.  pkHash mustbe 20
// bytes.
func NewAddressPubKeyHash(pkHash []byte, net *chaincfg.Params) (*AddressPubKeyHash, error) {
	return newAddressPubKeyHash(pkHash, net.PubKeyHashAddrID, net.Base58CksumHasher)
}

// newAddressPubKeyHash is the internal API to create a pubkey hash address
// with a known leading identifier byte for a network, rather than looking
// it up through its parameters.  This is useful when creating a new address
// structure from a string encoding where the identifer byte is already
// known.
func newAddressPubKeyHash(pkHash, netID []byte, hasher base58.CksumHasher) (*AddressPubKeyHash, error) {
	// Check for a valid pubkey hash length.
	if len(pkHash) != ripemd160.Size {
		return nil, errors.New("pkHash must be 20 bytes")
	}

	addr := &AddressPubKeyHash{}
	copy(addr.hash[:], pkHash)
	addr.netID = make([]byte, len(netID))
	copy(addr.netID, netID)
	addr.cksumHasher = hasher
	return addr, nil
}

// EncodeAddress returns the string encoding of a pay-to-pubkey-hash
// address.  Part of the Address interface.
func (a *AddressPubKeyHash) EncodeAddress() string {
	return encodeAddress(a.hash[:], a.netID, a.cksumHasher)
}

// ScriptAddress returns the bytes to be included in a txout script to pay
// to a pubkey hash.  Part of the Address interface.
func (a *AddressPubKeyHash) ScriptAddress() []byte {
	return a.hash[:]
}

// IsForNet returns whether or not the pay-to-pubkey-hash address is associated
// with the passed bitcoin network.
func (a *AddressPubKeyHash) IsForNet(net *chaincfg.Params) bool {
	return bytes.Equal(a.netID, net.PubKeyHashAddrID)
}

// String returns a human-readable string for the pay-to-pubkey-hash address.
// This is equivalent to calling EncodeAddress, but is provided so the type can
// be used as a fmt.Stringer.
func (a *AddressPubKeyHash) String() string {
	return a.EncodeAddress()
}

// Hash160 returns the underlying array of the pubkey hash.  This can be useful
// when an array is more appropiate than a slice (for example, when used as map
// keys).
func (a *AddressPubKeyHash) Hash160() *[ripemd160.Size]byte {
	return &a.hash
}

// AddressScriptHash is an Address for a pay-to-script-hash (P2SH)
// transaction.
type AddressScriptHash struct {
	hash        [ripemd160.Size]byte
	netID       []byte
	cksumHasher base58.CksumHasher
}

// NewAddressScriptHash returns a new AddressScriptHash.
func NewAddressScriptHash(serializedScript []byte, net *chaincfg.Params) (*AddressScriptHash, error) {
	scriptHash := CksumHashGen(net.Base58CksumHasher, serializedScript)
	return newAddressScriptHashFromHash(scriptHash, net.ScriptHashAddrID, net.Base58CksumHasher)
}

// NewAddressScriptHashFromHash returns a new AddressScriptHash.  scriptHash
// must be 20 bytes.
func NewAddressScriptHashFromHash(scriptHash []byte, net *chaincfg.Params) (*AddressScriptHash, error) {
	return newAddressScriptHashFromHash(scriptHash, net.ScriptHashAddrID, net.Base58CksumHasher)
}

// newAddressScriptHashFromHash is the internal API to create a script hash
// address with a known leading identifier byte for a network, rather than
// looking it up through its parameters.  This is useful when creating a new
// address structure from a string encoding where the identifer byte is already
// known.
func newAddressScriptHashFromHash(scriptHash, netID []byte, hasher base58.CksumHasher) (*AddressScriptHash, error) {
	// Check for a valid script hash length.
	if len(scriptHash) != ripemd160.Size {
		return nil, errors.New("scriptHash must be 20 bytes")
	}

	addr := &AddressScriptHash{}
	copy(addr.hash[:], scriptHash)
	addr.netID = make([]byte, len(netID))
	copy(addr.netID, netID)
	addr.cksumHasher = hasher
	return addr, nil
}

// EncodeAddress returns the string encoding of a pay-to-script-hash
// address.  Part of the Address interface.
func (a *AddressScriptHash) EncodeAddress() string {
	return encodeAddress(a.hash[:], a.netID, a.cksumHasher)
}

// ScriptAddress returns the bytes to be included in a txout script to pay
// to a script hash.  Part of the Address interface.
func (a *AddressScriptHash) ScriptAddress() []byte {
	return a.hash[:]
}

// IsForNet returns whether or not the pay-to-script-hash address is associated
// with the passed bitcoin network.
func (a *AddressScriptHash) IsForNet(net *chaincfg.Params) bool {
	return bytes.Equal(a.netID, net.ScriptHashAddrID)
}

// String returns a human-readable string for the pay-to-script-hash address.
// This is equivalent to calling EncodeAddress, but is provided so the type can
// be used as a fmt.Stringer.
func (a *AddressScriptHash) String() string {
	return a.EncodeAddress()
}

// Hash160 returns the underlying array of the script hash.  This can be useful
// when an array is more appropiate than a slice (for example, when used as map
// keys).
func (a *AddressScriptHash) Hash160() *[ripemd160.Size]byte {
	return &a.hash
}

// PubKeyFormat describes what format to use for a pay-to-pubkey address.
type PubKeyFormat int

const (
	// PKFUncompressed indicates the pay-to-pubkey address format is an
	// uncompressed public key.
	PKFUncompressed PubKeyFormat = iota

	// PKFCompressed indicates the pay-to-pubkey address format is a
	// compressed public key.
	PKFCompressed

	// PKFHybrid indicates the pay-to-pubkey address format is a hybrid
	// public key.
	PKFHybrid
)

// AddressPubKey is an Address for a pay-to-pubkey transaction.
type AddressPubKey struct {
	pubKeyFormat PubKeyFormat
	pubKey       *btcec.PublicKey
	pubKeyHashID []byte
	cksumHasher  base58.CksumHasher
}

// NewAddressPubKey returns a new AddressPubKey which represents a pay-to-pubkey
// address.  The serializedPubKey parameter must be a valid pubkey and can be
// uncompressed, compressed, or hybrid.
func NewAddressPubKey(serializedPubKey []byte, net *chaincfg.Params) (*AddressPubKey, error) {
	pubKey, err := btcec.ParsePubKey(serializedPubKey, btcec.S256())
	if err != nil {
		return nil, err
	}

	// Set the format of the pubkey.  This probably should be returned
	// from btcec, but do it here to avoid API churn.  We already know the
	// pubkey is valid since it parsed above, so it's safe to simply examine
	// the leading byte to get the format.
	pkFormat := PKFUncompressed
	switch serializedPubKey[0] {
	case 0x02, 0x03:
		pkFormat = PKFCompressed
	case 0x06, 0x07:
		pkFormat = PKFHybrid
	}

	addr := &AddressPubKey{
		pubKeyFormat: pkFormat,
		pubKey:       pubKey,
	}
	addr.pubKeyHashID = make([]byte, len(net.PubKeyHashAddrID))
	copy(addr.pubKeyHashID, net.PubKeyHashAddrID)
	addr.cksumHasher = net.Base58CksumHasher

	return addr, nil
}

// serialize returns the serialization of the public key according to the
// format associated with the address.
func (a *AddressPubKey) serialize() []byte {
	switch a.pubKeyFormat {
	default:
		fallthrough
	case PKFUncompressed:
		return a.pubKey.SerializeUncompressed()

	case PKFCompressed:
		return a.pubKey.SerializeCompressed()

	case PKFHybrid:
		return a.pubKey.SerializeHybrid()
	}
}

// EncodeAddress returns the string encoding of the public key as a
// pay-to-pubkey-hash.  Note that the public key format (uncompressed,
// compressed, etc) will change the resulting address.  This is expected since
// pay-to-pubkey-hash is a hash of the serialized public key which obviously
// differs with the format.  At the time of this writing, most Bitcoin addresses
// are pay-to-pubkey-hash constructed from the uncompressed public key.
//
// Part of the Address interface.
func (a *AddressPubKey) EncodeAddress() string {
	hash := CksumHashGen(a.cksumHasher, a.serialize())
	return encodeAddress(hash, a.pubKeyHashID, a.cksumHasher)
}

// ScriptAddress returns the bytes to be included in a txout script to pay
// to a public key.  Setting the public key format will affect the output of
// this function accordingly.  Part of the Address interface.
func (a *AddressPubKey) ScriptAddress() []byte {
	return a.serialize()
}

// IsForNet returns whether or not the pay-to-pubkey address is associated
// with the passed bitcoin network.
func (a *AddressPubKey) IsForNet(net *chaincfg.Params) bool {
	return bytes.Equal(a.pubKeyHashID, net.PubKeyHashAddrID)
}

// String returns the hex-encoded human-readable string for the pay-to-pubkey
// address.  This is not the same as calling EncodeAddress.
func (a *AddressPubKey) String() string {
	return hex.EncodeToString(a.serialize())
}

// Format returns the format (uncompressed, compressed, etc) of the
// pay-to-pubkey address.
func (a *AddressPubKey) Format() PubKeyFormat {
	return a.pubKeyFormat
}

// SetFormat sets the format (uncompressed, compressed, etc) of the
// pay-to-pubkey address.
func (a *AddressPubKey) SetFormat(pkFormat PubKeyFormat) {
	a.pubKeyFormat = pkFormat
}

// AddressPubKeyHash returns the pay-to-pubkey address converted to a
// pay-to-pubkey-hash address.  Note that the public key format (uncompressed,
// compressed, etc) will change the resulting address.  This is expected since
// pay-to-pubkey-hash is a hash of the serialized public key which obviously
// differs with the format.  At the time of this writing, most Bitcoin addresses
// are pay-to-pubkey-hash constructed from the uncompressed public key.
func (a *AddressPubKey) AddressPubKeyHash() *AddressPubKeyHash {
	addr := &AddressPubKeyHash{
		netID:       a.pubKeyHashID,
		cksumHasher: a.cksumHasher,
	}

	hash := CksumHashGen(a.cksumHasher, a.serialize())
	copy(addr.hash[:], hash)
	return addr
}

// PubKey returns the underlying public key for the address.
func (a *AddressPubKey) PubKey() *btcec.PublicKey {
	return a.pubKey
}

// AddressWitnessPubKeyHash is an Address for a pay-to-witness-pubkey-hash
// (P2WPKH) output. See BIP 173 for further details regarding native segregated
// witness address encoding:
// https://github.com/bitcoin/bips/blob/master/bip-0173.mediawiki
type AddressWitnessPubKeyHash struct {
	hrp            string
	witnessVersion byte
	witnessProgram [20]byte
}

// NewAddressWitnessPubKeyHash returns a new AddressWitnessPubKeyHash.
func NewAddressWitnessPubKeyHash(witnessProg []byte, net *chaincfg.Params) (*AddressWitnessPubKeyHash, error) {
	return newAddressWitnessPubKeyHash(net.Bech32HRPSegwit, witnessProg)
}

// newAddressWitnessPubKeyHash is an internal helper function to create an
// AddressWitnessPubKeyHash with a known human-readable part, rather than
// looking it up through its parameters.
func newAddressWitnessPubKeyHash(hrp string, witnessProg []byte) (*AddressWitnessPubKeyHash, error) {
	// Check for valid program length for witness version 0, which is 20
	// for P2WPKH.
	if len(witnessProg) != 20 {
		return nil, errors.New("witness program must be 20 " +
			"bytes for p2wpkh")
	}

	addr := &AddressWitnessPubKeyHash{
		hrp:            strings.ToLower(hrp),
		witnessVersion: 0x00,
	}

	copy(addr.witnessProgram[:], witnessProg)

	return addr, nil
}

// EncodeAddress returns the bech32 string encoding of an
// AddressWitnessPubKeyHash.
// Part of the Address interface.
func (a *AddressWitnessPubKeyHash) EncodeAddress() string {
	str, err := encodeSegWitAddress(a.hrp, a.witnessVersion,
		a.witnessProgram[:])
	if err != nil {
		return ""
	}
	return str
}

// ScriptAddress returns the witness program for this address.
// Part of the Address interface.
func (a *AddressWitnessPubKeyHash) ScriptAddress() []byte {
	return a.witnessProgram[:]
}

// IsForNet returns whether or not the AddressWitnessPubKeyHash is associated
// with the passed bitcoin network.
// Part of the Address interface.
func (a *AddressWitnessPubKeyHash) IsForNet(net *chaincfg.Params) bool {
	return a.hrp == net.Bech32HRPSegwit
}

// String returns a human-readable string for the AddressWitnessPubKeyHash.
// This is equivalent to calling EncodeAddress, but is provided so the type
// can be used as a fmt.Stringer.
// Part of the Address interface.
func (a *AddressWitnessPubKeyHash) String() string {
	return a.EncodeAddress()
}

// Hrp returns the human-readable part of the bech32 encoded
// AddressWitnessPubKeyHash.
func (a *AddressWitnessPubKeyHash) Hrp() string {
	return a.hrp
}

// WitnessVersion returns the witness version of the AddressWitnessPubKeyHash.
func (a *AddressWitnessPubKeyHash) WitnessVersion() byte {
	return a.witnessVersion
}

// WitnessProgram returns the witness program of the AddressWitnessPubKeyHash.
func (a *AddressWitnessPubKeyHash) WitnessProgram() []byte {
	return a.witnessProgram[:]
}

// Hash160 returns the witness program of the AddressWitnessPubKeyHash as a
// byte array.
func (a *AddressWitnessPubKeyHash) Hash160() *[20]byte {
	return &a.witnessProgram
}

// AddressWitnessScriptHash is an Address for a pay-to-witness-script-hash
// (P2WSH) output. See BIP 173 for further details regarding native segregated
// witness address encoding:
// https://github.com/bitcoin/bips/blob/master/bip-0173.mediawiki
type AddressWitnessScriptHash struct {
	hrp            string
	witnessVersion byte
	witnessProgram [32]byte
}

type AddressWitnessTaproot struct {
	AddressWitnessScriptHash
}

type AddressWitnessUnknown struct {
	hrp            string
	witnessVersion byte
	witnessProgram []byte
}

// EncodeAddress returns the bech32 string encoding of an
// AddressWitnessUnknown.
// Part of the Address interface.
func (a *AddressWitnessUnknown) EncodeAddress() string {
	str, err := encodeSegWitAddress(a.hrp, a.witnessVersion, a.witnessProgram)
	if err != nil {
		return ""
	}
	return str
}

// ScriptAddress returns the witness program for this address.
// Part of the Address interface.
func (a *AddressWitnessUnknown) ScriptAddress() []byte {
	return a.witnessProgram
}

// IsForNet returns whether or not the AddressWitnessUnknown is associated
// with the passed bitcoin network.
// Part of the Address interface.
func (a *AddressWitnessUnknown) IsForNet(net *chaincfg.Params) bool {
	return a.hrp == net.Bech32HRPSegwit
}

// String returns a human-readable string for the AddressWitnessPubKeyHash.
// This is equivalent to calling EncodeAddress, but is provided so the type
// can be used as a fmt.Stringer.
// Part of the Address interface.
func (a *AddressWitnessUnknown) String() string {
	return a.EncodeAddress()
}

// Hrp returns the human-readable part of the bech32 encoded
// AddressWitnessScriptHash.
func (a *AddressWitnessUnknown) Hrp() string {
	return a.hrp
}

// WitnessVersion returns the witness version of the AddressWitnessScriptHash.
func (a *AddressWitnessUnknown) WitnessVersion() byte {
	return a.witnessVersion
}

// WitnessProgram returns the witness program of the AddressWitnessScriptHash.
func (a *AddressWitnessUnknown) WitnessProgram() []byte {
	return a.witnessProgram
}

// NewAddressWitnessScriptHash returns a new AddressWitnessPubKeyHash.
func NewAddressWitnessScriptHash(witnessProg []byte, net *chaincfg.Params) (*AddressWitnessScriptHash, error) {
	return newAddressWitnessScriptHash(net.Bech32HRPSegwit, witnessProg)
}

// newAddressWitnessScriptHash is an internal helper function to create an
// AddressWitnessScriptHash with a known human-readable part, rather than
// looking it up through its parameters.
func newAddressWitnessScriptHash(hrp string, witnessProg []byte) (*AddressWitnessScriptHash, error) {
	// Check for valid program length for witness version 0, which is 32
	// for P2WSH.
	if len(witnessProg) != 32 {
		return nil, errors.New("witness program must be 32 " +
			"bytes for p2wsh")
	}

	addr := &AddressWitnessScriptHash{
		hrp:            strings.ToLower(hrp),
		witnessVersion: 0,
	}

	copy(addr.witnessProgram[:], witnessProg)

	return addr, nil
}

// NewAddressWitnessTaproot returns a new AddressWitnessTaproot.
func NewAddressWitnessTaproot(witnessProg []byte, net *chaincfg.Params) (*AddressWitnessTaproot, error) {
	return newAddressWitnessTaproot(net.Bech32HRPSegwit, witnessProg)
}

// newAddressWitnessTaproot returns a new AddressWitnessTaproot.
func newAddressWitnessTaproot(hrp string, witnessProg []byte) (*AddressWitnessTaproot, error) {
	// Check for valid program length for witness version 0, which is 32 for taproot
	if len(witnessProg) != 32 {
		return nil, errors.New("witness program must be 32 bytes for witness_v1_taproot")
	}

	addr := AddressWitnessTaproot{}
	addr.hrp = strings.ToLower(hrp)
	addr.witnessVersion = 1
	copy(addr.witnessProgram[:], witnessProg)

	return &addr, nil
}

// NewAddressWitnessUnknown returns a new AddressWitnessUnknown.
func NewAddressWitnessUnknown(witnessVer byte, witnessProg []byte, net *chaincfg.Params) (*AddressWitnessUnknown, error) {
	return newAddressWitnessUnknown(net.Bech32HRPSegwit, witnessVer, witnessProg)
}

// newAddressWitnessUnknown returns a new AddressWitnessUnknown.
func newAddressWitnessUnknown(hrp string, witnessVer byte, witnessProg []byte) (*AddressWitnessUnknown, error) {
	addr := AddressWitnessUnknown{
		hrp:            strings.ToLower(hrp),
		witnessVersion: witnessVer,
		witnessProgram: witnessProg,
	}
	return &addr, nil
}

// EncodeAddress returns the bech32 string encoding of an
// AddressWitnessScriptHash.
// Part of the Address interface.
func (a *AddressWitnessScriptHash) EncodeAddress() string {
	str, err := encodeSegWitAddress(a.hrp, a.witnessVersion,
		a.witnessProgram[:])
	if err != nil {
		return ""
	}
	return str
}

// ScriptAddress returns the witness program for this address.
// Part of the Address interface.
func (a *AddressWitnessScriptHash) ScriptAddress() []byte {
	return a.witnessProgram[:]
}

// IsForNet returns whether or not the AddressWitnessScriptHash is associated
// with the passed bitcoin network.
// Part of the Address interface.
func (a *AddressWitnessScriptHash) IsForNet(net *chaincfg.Params) bool {
	return a.hrp == net.Bech32HRPSegwit
}

// String returns a human-readable string for the AddressWitnessScriptHash.
// This is equivalent to calling EncodeAddress, but is provided so the type
// can be used as a fmt.Stringer.
// Part of the Address interface.
func (a *AddressWitnessScriptHash) String() string {
	return a.EncodeAddress()
}

// Hrp returns the human-readable part of the bech32 encoded
// AddressWitnessScriptHash.
func (a *AddressWitnessScriptHash) Hrp() string {
	return a.hrp
}

// WitnessVersion returns the witness version of the AddressWitnessScriptHash.
func (a *AddressWitnessScriptHash) WitnessVersion() byte {
	return a.witnessVersion
}

// WitnessProgram returns the witness program of the AddressWitnessScriptHash.
func (a *AddressWitnessScriptHash) WitnessProgram() []byte {
	return a.witnessProgram[:]
}
