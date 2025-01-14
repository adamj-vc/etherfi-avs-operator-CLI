package witnesschain

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/etherfi-protocol/etherfi-avs-operator-tool/src/config"
	"github.com/etherfi-protocol/etherfi-avs-operator-tool/src/eigenlayer"
	"github.com/etherfi-protocol/etherfi-avs-operator-tool/src/etherfi"
	"github.com/etherfi-protocol/etherfi-avs-operator-tool/src/gnosis"
	"github.com/etherfi-protocol/etherfi-avs-operator-tool/src/utils"
)

// API handle for all core witness chain functionality
type API struct {
	Client *ethclient.Client

	OperatorRegistry          *WitnessChainOperatorRegistry
	OperatorRegistryAddress   common.Address
	WitnessHub                *WitnessChainWitnessHub
	WitnessHubAddress         common.Address
	ServiceManagerAddress     common.Address
	AvsOperatorManagerAddress common.Address

	EigenlayerAPI *eigenlayer.API
}

func New(cfg config.Config, rpcClient *ethclient.Client) *API {

	operatorRegistry, _ := NewWitnessChainOperatorRegistry(cfg.WitnessChainOperatorRegistryAddress, rpcClient)
	witnessHub, _ := NewWitnessChainWitnessHub(cfg.WitnessChainWitnessHubAddress, rpcClient)

	return &API{
		Client: rpcClient,

		OperatorRegistry:          operatorRegistry,
		OperatorRegistryAddress:   cfg.WitnessChainOperatorRegistryAddress,
		WitnessHub:                witnessHub,
		WitnessHubAddress:         cfg.WitnessChainWitnessHubAddress,
		AvsOperatorManagerAddress: cfg.AvsOperatorManagerAddress,
		EigenlayerAPI:             eigenlayer.New(cfg, rpcClient),

		// witnessHub serves as the ServiceManager for this AVS
		ServiceManagerAddress: cfg.WitnessChainWitnessHubAddress,
	}
}

// Info that node operator must supply to the ether.fi admin for registration
type RegistrationInfo struct {
	OperatorID                int64
	WatchtowerAddress         common.Address
	WatchtowerSignature       string
	WatchtowerSignatureSalt   []byte
	WatchtowerSignatureExpiry *big.Int
}

// PrepareRegistration aggregates all required info from the node operator that
// the ether.fi admin will need to register them to the AVS
func (wc *API) PrepareRegistration(operator *etherfi.Operator, watchtowerKey *ecdsa.PrivateKey) error {

	// compute the watchtower registration digest
	expiry := new(big.Int).SetInt64(time.Now().Add(24 * time.Hour * 10).Unix())
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("gererating random salt: %w", err)
	}
	registrationDigest, err := wc.OperatorRegistry.CalculateWatchtowerRegistrationMessageHash(nil, operator.Address, [32]byte(salt), expiry)
	if err != nil {
		return fmt.Errorf("calculating watchtower registration digest: %w", err)
	}

	// sign the digest with the watchtower key
	signed, err := utils.SignDigestECDSA(registrationDigest[:], watchtowerKey)
	if err != nil {
		return fmt.Errorf("signing digest: %w", err)
	}

	ri := RegistrationInfo{
		OperatorID:                operator.ID,
		WatchtowerAddress:         crypto.PubkeyToAddress(watchtowerKey.PublicKey),
		WatchtowerSignature:       "0x" + hex.EncodeToString(signed),
		WatchtowerSignatureSalt:   salt,
		WatchtowerSignatureExpiry: expiry,
	}

	return utils.ExportJSON("witnesschain-prepare-registration", operator.ID, ri)
}

// RegisterOperator is used by the ether.fi admin to register a node operator's AvsOperator contract
// with the Witness Chain AVS.
func (wc *API) RegisterOperator(operator *etherfi.Operator, signingKey *ecdsa.PrivateKey) error {

	// generate and sign registration hash with admin ecdsa key
	signature, err := wc.EigenlayerAPI.GenerateAndSignRegistrationDigest(operator, wc.ServiceManagerAddress, signingKey)
	if err != nil {
		return fmt.Errorf("signing registration digest: %w", err)
	}

	// convert to types expected by contract call
	sigWithSaltAndExpiry := ISignatureUtilsSignatureWithSaltAndExpiry{
		Signature: signature.Signature,
		Salt:      signature.Salt,
		Expiry:    signature.Expiry,
	}

	// manually pack tx data since we are submitting via gnosis instead of directly
	hubABI, err := WitnessChainWitnessHubMetaData.GetAbi()
	if err != nil {
		return fmt.Errorf("fetching abi: %w", err)
	}
	input, err := hubABI.Pack("registerOperatorToAVS", operator.Address, sigWithSaltAndExpiry)
	if err != nil {
		return fmt.Errorf("packing input: %w", err)
	}

	// wrap the inner call to be forwarded via AvsOperatorManager
	adminCall, err := utils.PackForwardCallForAdmin(operator.ID, input, wc.WitnessHubAddress)
	if err != nil {
		return fmt.Errorf("wrapping call for admin: %w", err)
	}

	// output in gnosis compatible format
	batch := gnosis.NewSingleTxBatch(adminCall, wc.AvsOperatorManagerAddress, fmt.Sprintf("witness-chain-register-%d", operator.ID))
	return utils.ExportJSON("witness-chain-register-gnosis", operator.ID, batch)
}

func (wc *API) RegisterWatchtower(operator *etherfi.Operator, info *RegistrationInfo) error {

	// parse watchtower signature
	if strings.HasPrefix(info.WatchtowerSignature, "0x") {
		info.WatchtowerSignature = info.WatchtowerSignature[2:]
	}
	watchtowerSignature, err := hex.DecodeString(info.WatchtowerSignature)
	if err != nil {
		return fmt.Errorf("invalid watchtower signature")
	}

	// manually pack tx data since we are submitting via gnosis instead of directly
	witnessABI, err := WitnessChainOperatorRegistryMetaData.GetAbi()
	if err != nil {
		return fmt.Errorf("fetching abi: %w", err)
	}

	// pack operatorRegistry.registerWatchtowerAsOperator()
	calldata, err := witnessABI.Pack("registerWatchtowerAsOperator", info.WatchtowerAddress, [32]byte(info.WatchtowerSignatureSalt), info.WatchtowerSignatureExpiry, watchtowerSignature)
	if err != nil {
		return fmt.Errorf("packing input: %w", err)
	}

	// wrap the inner call to be forwarded via AvsOperatorManager
	adminCall, err := utils.PackForwardCallForAdmin(operator.ID, calldata, wc.OperatorRegistryAddress)
	if err != nil {
		return fmt.Errorf("wrapping call for admin: %w", err)
	}

	// output in gnosis compatible format
	batch := gnosis.NewSingleTxBatch(adminCall, wc.AvsOperatorManagerAddress, fmt.Sprintf("witness-chain-register-watchtower-%d", operator.ID))
	return utils.ExportJSON("witness-chain-register-watchtower-gnosis", operator.ID, batch)
}
