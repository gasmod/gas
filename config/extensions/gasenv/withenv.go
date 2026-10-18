package gasenv

import "github.com/gasmod/gas"

// WithGasEnv is embedded in a configuration struct to carry the environment
// the extension resolved; its GasEnv field binds from the GasEnv key. It is
// gas.WithGasEnv: new code should embed that directly, so a module does not
// import config just for the embedding.
type WithGasEnv = gas.WithGasEnv
