package types

import (
	fmt "fmt"
	"mercury/common"
	"mercury/common/cosmos"
)

func NewProvider(pubkey common.PubKey, chain common.Chain) Provider {
	return Provider{
		PubKey: pubkey,
		Chain:  chain,
		Bond:   cosmos.ZeroInt(),
	}
}

func (p Provider) Key() string {
	return fmt.Sprintf("%s/%s", p.PubKey, p.Chain)
}

func NewContract(pubkey common.PubKey, chain common.Chain, client cosmos.AccAddress) Contract {
	return Contract{
		ProviderPubKey: pubkey,
		Chain:          chain,
		ClientAddress:  client,
		Deposit:        cosmos.ZeroInt(),
		Paid:           cosmos.ZeroInt(),
	}
}

func (c Contract) Key() string {
	return fmt.Sprintf("%s/%s/%s", c.ProviderPubKey, c.Chain, c.ClientAddress)
}

func (c Contract) Expiration() int64 {
	return c.Height + c.Duration
}

func (c Contract) IsOpen(h int64) bool {
	if c.IsEmpty() {
		return false
	}
	if c.Expiration() < h {
		return false
	}
	return true
}

func (c Contract) IsClose(h int64) bool {
	return !c.IsOpen(h)
}

func (c Contract) IsEmpty() bool {
	return c.Height == 0
}
