//go:build linux || darwin

package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// FileRepository is a durable single-host journal for Linux/macOS. Use a local
// filesystem with flock, atomic rename and fsync semantics, not NFS/object storage.
// Directory must be provisioned durably and restricted to the service beforehand.
// ponytail: each operation reads the whole journal; use a database implementation
// of Repository when transaction volume requires indexed access.
type FileRepository struct{ Directory string }

func (r FileRepository) locked(ctx context.Context, fn func(map[string]Prepared) (bool, error)) error {
	if r.Directory == "" {
		return errors.New("journal directory required")
	}
	info, err := os.Stat(r.Directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("journal path must be an existing directory")
	}
	lock, err := os.OpenFile(filepath.Join(r.Directory, "journal.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(r.Directory, "transactions.json")
	records := map[string]Prepared{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &records); err != nil || records == nil {
			return errors.New("corrupt transaction journal")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seen := map[string]bool{}
	for id, p := range records {
		if id != p.ID {
			return errors.New("corrupt journal ID")
		}
		if err := validatePrepared(p); err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%s/%d", p.ChainID, p.From.Hex(), p.Nonce)
		if seen[key] {
			return errors.New("corrupt journal nonce collision")
		}
		seen[key] = true
	}
	changed, err := fn(records)
	if err != nil {
		return err
	}
	// Complete an ambiguous previous rename even when this operation only reads.
	if !changed {
		return syncDirectory(r.Directory)
	}
	data, err = json.Marshal(records)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.Directory, ".transactions-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDirectory(r.Directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (r FileRepository) Get(ctx context.Context, id string) (Prepared, error) {
	var p Prepared
	err := r.locked(ctx, func(records map[string]Prepared) (bool, error) {
		var ok bool
		p, ok = records[id]
		if !ok {
			return false, ErrNotFound
		}
		return false, nil
	})
	return p, err
}

func (r FileRepository) NextNonce(ctx context.Context, chain string, from common.Address, floor uint64) (uint64, error) {
	next := floor
	err := r.locked(ctx, func(records map[string]Prepared) (bool, error) {
		for _, p := range records {
			if p.ChainID == chain && p.From == from && p.Nonce >= next {
				if p.Nonce == math.MaxUint64 {
					return false, errors.New("nonce exhausted")
				}
				next = p.Nonce + 1
			}
		}
		return false, nil
	})
	return next, err
}

func (r FileRepository) Put(ctx context.Context, p Prepared) error {
	if err := validatePrepared(p); err != nil {
		return err
	}
	return r.locked(ctx, func(records map[string]Prepared) (bool, error) {
		if old, ok := records[p.ID]; ok {
			if old.Intent != p.Intent || old.ChainID != p.ChainID || old.From != p.From || old.Nonce != p.Nonce || old.Hash != p.Hash || !bytes.Equal(old.Raw, p.Raw) {
				return false, ErrConflict
			}
			return false, nil
		}
		for _, old := range records {
			if old.ChainID == p.ChainID && old.From == p.From && old.Nonce == p.Nonce {
				return false, fmt.Errorf("%w: %d", ErrNonceConflict, p.Nonce)
			}
		}
		records[p.ID] = p
		return true, nil
	})
}

func validatePrepared(p Prepared) error {
	chain, ok := new(big.Int).SetString(p.ChainID, 10)
	if !ok || chain.Sign() <= 0 || chain.BitLen() > 256 || p.ID == "" || p.Intent == (common.Hash{}) {
		return errors.New("invalid prepared transaction")
	}
	if _, err := (Engine{ChainID: chain}).validate(p); err != nil {
		return err
	}
	return nil
}
