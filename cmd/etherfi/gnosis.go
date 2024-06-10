package main

import (
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

type SubTransaction struct {
	Target common.Address
	Value  *big.Int
	Data   []byte
}

type GnosisMetadata struct {
	Name string `json:"name"`
}

type GnosisBatch struct {
	Version      string           `json:"version"`
	ChainId      string           `json:"chainId"`
	Meta         GnosisMetadata   `json:"meta"`
	Transactions []SubTransaction `json:"transactions"`
}

func (s *SubTransaction) MarshalJSON() ([]byte, error) {
	type out struct {
		To    string `json:"to"`
		Value string `json:"value"`
		Data  string `json:"data"`
	}

	return json.Marshal(&out{
		To:    s.Target.Hex(),
		Value: s.Value.String(),
		Data:  "0x" + hex.EncodeToString(s.Data),
	})
}

type GnosisOutput struct {
	Version      string
	ChainId      string
	Transactions []SubTransaction
}

func (b *GnosisBatch) AddTransaction(tx SubTransaction) {
	b.Transactions = append(b.Transactions, tx)
}

func (b *GnosisBatch) AddTransactions(txs []SubTransaction) {
	b.Transactions = append(b.Transactions, txs...)
}
