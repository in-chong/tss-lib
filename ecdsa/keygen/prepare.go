// Copyright © 2019 Binance
//
// This file is part of Binance. The full Binance copyright notice, including
// terms governing use, modification, and redistribution, is contained in the
// file LICENSE at the root of the source code distribution tree.

package keygen

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"math/big"
	"runtime"
	"time"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/crypto/paillier"
	// "encoding/json"
	//  "fmt"
)

const (
	// Using a modulus length of 2048 is recommended in the GG18 spec
	paillierModulusLen = 2048
	// Two 1024-bit safe primes to produce NTilde
	safePrimeBitLen = 1024
	// Ticker for printing log statements while generating primes/modulus
	logProgressTickInterval = 8 * time.Second
	// Safe big len using random for ssid test s
	SafeBitLen = 1024
)

// GeneratePreParams finds two safe primes and computes the Paillier secret required for the protocol.
// This can be a time consuming process so it is recommended to do it out-of-band.
// If not specified, a concurrency value equal to the number of available CPU cores will be used.
// If pre-parameters could not be generated before the timeout, an error is returned.
func GeneratePreParams(timeout time.Duration, optionalConcurrency ...int) (*LocalPreParams, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return GeneratePreParamsWithContext(ctx, optionalConcurrency...)
}

// GeneratePreParams finds two safe primes and computes the Paillier secret required for the protocol.
// This can be a time consuming process so it is recommended to do it out-of-band.
// If not specified, a concurrency value equal to the number of available CPU cores will be used.
// If pre-parameters could not be generated before the context is done, an error is returned.
func GeneratePreParamsWithContext(ctx context.Context,optionalConcurrency ...int) (*LocalPreParams, error) {
	return GeneratePreParamsWithContextAndRandom(ctx, rand.Reader, optionalConcurrency...)
}

// GeneratePreParams finds two safe primes and computes the Paillier secret required for the protocol.
// This can be a time consuming process so it is recommended to do it out-of-band.
// If not specified, a concurrency value equal to the number of available CPU cores will be used.
// If pre-parameters could not be generated before the context is done, an error is returned.
func GeneratePreParamsWithContextAndRandom(ctx context.Context, rand io.Reader, optionalConcurrency ...int) (*LocalPreParams, error) {
	common.Logger.Info("进来啊GeneratePreParamsWithContextAndRandom===》")
	devMode := true
	if devMode {
		//fixtures, _, err := LoadKeygenTestFixtures(1)
		preParams, err := LoadPreParams()
		if err != nil {
			common.Logger.Info("加载fixture出错: ", err)
			return nil, err
		}
		return preParams, nil
		// if len(fixtures) == 0 {
		// 	common.Logger.Info("未加载到任何fixture")
		// 	return nil, errors.New("no fixtures loaded")
		// }

	// 	b, err := json.MarshalIndent(fixtures, "", "  ")
    //    if err != nil {
    //         fmt.Println("marshal error:", err)
    //     } else {
    //         fmt.Println("LoadKeygenTestFixtures001===>", string(b))
    //     }

	// 	return &fixtures[0].LocalPreParams, nil
	} else{
	common.Logger.Info("进来2===>")
		var concurrency int
		if 0 < len(optionalConcurrency) {
			if 1 < len(optionalConcurrency) {
				panic(errors.New("GeneratePreParams: expected 0 or 1 item in `optionalConcurrency`"))
			}
			concurrency = optionalConcurrency[0]
		} else {
			concurrency = runtime.NumCPU()
		}
		if concurrency /= 3; concurrency < 1 {
			concurrency = 1
		}
	
		// prepare for concurrent Paillier and safe prime generation
		paiCh := make(chan *paillier.PrivateKey, 1)
		sgpCh := make(chan []*common.GermainSafePrime, 1)
	
		// 4. generate Paillier public key E_i, private key and proof
		go func(ch chan<- *paillier.PrivateKey) {
			common.Logger.Info("generating the Paillier modulus, please wait...")
			start := time.Now()
			// more concurrency weight is assigned here because the paillier primes have a requirement of having "large" P-Q
			PiPaillierSk, _, err := paillier.GenerateKeyPair(ctx, rand, paillierModulusLen, concurrency*2)
			if err != nil {
				ch <- nil
				return
			}
			common.Logger.Infof("paillier modulus generated. took %s\n", time.Since(start))
			ch <- PiPaillierSk
		}(paiCh)
	
		// 5-7. generate safe primes for ZKPs used later on
		go func(ch chan<- []*common.GermainSafePrime) {
			var err error
			common.Logger.Info("generating the safe primes for the signing proofs, please wait...")
			start := time.Now()
			sgps, err := common.GetRandomSafePrimesConcurrent(ctx, safePrimeBitLen, 2, concurrency, rand)
			if err != nil {
				ch <- nil
				return
			}
			common.Logger.Infof("safe primes generated. took %s\n", time.Since(start))
			ch <- sgps
		}(sgpCh)
	
		// this ticker will print a log statement while the generating is still in progress
		logProgressTicker := time.NewTicker(logProgressTickInterval)
	
		// errors can be thrown in the following code; consume chans to end goroutines here
		var sgps []*common.GermainSafePrime
		var paiSK *paillier.PrivateKey
	consumer:
		for {
			select {
			case <-logProgressTicker.C:
				common.Logger.Info("still generating primes...")
			case sgps = <-sgpCh:
				if sgps == nil ||
					sgps[0] == nil || sgps[1] == nil ||
					!sgps[0].Prime().ProbablyPrime(30) || !sgps[1].Prime().ProbablyPrime(30) ||
					!sgps[0].SafePrime().ProbablyPrime(30) || !sgps[1].SafePrime().ProbablyPrime(30) {
					return nil, errors.New("timeout or error while generating the safe primes")
				}
				if paiSK != nil {
					break consumer
				}
			case paiSK = <-paiCh:
				if paiSK == nil {
					return nil, errors.New("timeout or error while generating the Paillier secret key")
				}
				if sgps != nil {
					break consumer
				}
			}
		}
		logProgressTicker.Stop()
	
		P, Q := sgps[0].SafePrime(), sgps[1].SafePrime()
		NTildei := new(big.Int).Mul(P, Q)
		modNTildeI := common.ModInt(NTildei)
	
		p, q := sgps[0].Prime(), sgps[1].Prime()
		modPQ := common.ModInt(new(big.Int).Mul(p, q))
		f1 := common.GetRandomPositiveRelativelyPrimeInt(rand, NTildei)
		alpha := common.GetRandomPositiveRelativelyPrimeInt(rand, NTildei)
		beta := modPQ.ModInverse(alpha)
		h1i := modNTildeI.Mul(f1, f1)
		h2i := modNTildeI.Exp(h1i, alpha)
	
		preParams := &LocalPreParams{
			PaillierSK: paiSK,
			NTildei:    NTildei,
			H1i:        h1i,
			H2i:        h2i,
			Alpha:      alpha,
			Beta:       beta,
			P:          p,
			Q:          q,
		}
		return preParams, nil
	}
}
