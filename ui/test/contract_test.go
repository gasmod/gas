package test

import (
	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

//goland:noinspection GoVarAndConstTypeMayBeOmitted
var _ func(
	gas.TemplateProvider,
	*gas.Router,
	gas.ConfigProvider,
	gas.Logger,
) (*ui.Service, error) = ui.New[gas.TemplateProvider]()
