package coreservices_test

import (
	"errors"
	"testing"

	"github.com/gasmod/gas"
)

// Two constructor chains: sa001 needs sa002, which needs sa003, and so on,
// 256 constructors deep; the sb chain is 258 deep. Transient lifetimes keep
// each build nested inside the one that needs it.

type (
	sa001 struct{}
	sa002 struct{}
	sa003 struct{}
	sa004 struct{}
	sa005 struct{}
	sa006 struct{}
	sa007 struct{}
	sa008 struct{}
	sa009 struct{}
	sa010 struct{}
	sa011 struct{}
	sa012 struct{}
	sa013 struct{}
	sa014 struct{}
	sa015 struct{}
	sa016 struct{}
	sa017 struct{}
	sa018 struct{}
	sa019 struct{}
	sa020 struct{}
	sa021 struct{}
	sa022 struct{}
	sa023 struct{}
	sa024 struct{}
	sa025 struct{}
	sa026 struct{}
	sa027 struct{}
	sa028 struct{}
	sa029 struct{}
	sa030 struct{}
	sa031 struct{}
	sa032 struct{}
	sa033 struct{}
	sa034 struct{}
	sa035 struct{}
	sa036 struct{}
	sa037 struct{}
	sa038 struct{}
	sa039 struct{}
	sa040 struct{}
	sa041 struct{}
	sa042 struct{}
	sa043 struct{}
	sa044 struct{}
	sa045 struct{}
	sa046 struct{}
	sa047 struct{}
	sa048 struct{}
	sa049 struct{}
	sa050 struct{}
	sa051 struct{}
	sa052 struct{}
	sa053 struct{}
	sa054 struct{}
	sa055 struct{}
	sa056 struct{}
	sa057 struct{}
	sa058 struct{}
	sa059 struct{}
	sa060 struct{}
	sa061 struct{}
	sa062 struct{}
	sa063 struct{}
	sa064 struct{}
	sa065 struct{}
	sa066 struct{}
	sa067 struct{}
	sa068 struct{}
	sa069 struct{}
	sa070 struct{}
	sa071 struct{}
	sa072 struct{}
	sa073 struct{}
	sa074 struct{}
	sa075 struct{}
	sa076 struct{}
	sa077 struct{}
	sa078 struct{}
	sa079 struct{}
	sa080 struct{}
	sa081 struct{}
	sa082 struct{}
	sa083 struct{}
	sa084 struct{}
	sa085 struct{}
	sa086 struct{}
	sa087 struct{}
	sa088 struct{}
	sa089 struct{}
	sa090 struct{}
	sa091 struct{}
	sa092 struct{}
	sa093 struct{}
	sa094 struct{}
	sa095 struct{}
	sa096 struct{}
	sa097 struct{}
	sa098 struct{}
	sa099 struct{}
	sa100 struct{}
	sa101 struct{}
	sa102 struct{}
	sa103 struct{}
	sa104 struct{}
	sa105 struct{}
	sa106 struct{}
	sa107 struct{}
	sa108 struct{}
	sa109 struct{}
	sa110 struct{}
	sa111 struct{}
	sa112 struct{}
	sa113 struct{}
	sa114 struct{}
	sa115 struct{}
	sa116 struct{}
	sa117 struct{}
	sa118 struct{}
	sa119 struct{}
	sa120 struct{}
	sa121 struct{}
	sa122 struct{}
	sa123 struct{}
	sa124 struct{}
	sa125 struct{}
	sa126 struct{}
	sa127 struct{}
	sa128 struct{}
	sa129 struct{}
	sa130 struct{}
	sa131 struct{}
	sa132 struct{}
	sa133 struct{}
	sa134 struct{}
	sa135 struct{}
	sa136 struct{}
	sa137 struct{}
	sa138 struct{}
	sa139 struct{}
	sa140 struct{}
	sa141 struct{}
	sa142 struct{}
	sa143 struct{}
	sa144 struct{}
	sa145 struct{}
	sa146 struct{}
	sa147 struct{}
	sa148 struct{}
	sa149 struct{}
	sa150 struct{}
	sa151 struct{}
	sa152 struct{}
	sa153 struct{}
	sa154 struct{}
	sa155 struct{}
	sa156 struct{}
	sa157 struct{}
	sa158 struct{}
	sa159 struct{}
	sa160 struct{}
	sa161 struct{}
	sa162 struct{}
	sa163 struct{}
	sa164 struct{}
	sa165 struct{}
	sa166 struct{}
	sa167 struct{}
	sa168 struct{}
	sa169 struct{}
	sa170 struct{}
	sa171 struct{}
	sa172 struct{}
	sa173 struct{}
	sa174 struct{}
	sa175 struct{}
	sa176 struct{}
	sa177 struct{}
	sa178 struct{}
	sa179 struct{}
	sa180 struct{}
	sa181 struct{}
	sa182 struct{}
	sa183 struct{}
	sa184 struct{}
	sa185 struct{}
	sa186 struct{}
	sa187 struct{}
	sa188 struct{}
	sa189 struct{}
	sa190 struct{}
	sa191 struct{}
	sa192 struct{}
	sa193 struct{}
	sa194 struct{}
	sa195 struct{}
	sa196 struct{}
	sa197 struct{}
	sa198 struct{}
	sa199 struct{}
	sa200 struct{}
	sa201 struct{}
	sa202 struct{}
	sa203 struct{}
	sa204 struct{}
	sa205 struct{}
	sa206 struct{}
	sa207 struct{}
	sa208 struct{}
	sa209 struct{}
	sa210 struct{}
	sa211 struct{}
	sa212 struct{}
	sa213 struct{}
	sa214 struct{}
	sa215 struct{}
	sa216 struct{}
	sa217 struct{}
	sa218 struct{}
	sa219 struct{}
	sa220 struct{}
	sa221 struct{}
	sa222 struct{}
	sa223 struct{}
	sa224 struct{}
	sa225 struct{}
	sa226 struct{}
	sa227 struct{}
	sa228 struct{}
	sa229 struct{}
	sa230 struct{}
	sa231 struct{}
	sa232 struct{}
	sa233 struct{}
	sa234 struct{}
	sa235 struct{}
	sa236 struct{}
	sa237 struct{}
	sa238 struct{}
	sa239 struct{}
	sa240 struct{}
	sa241 struct{}
	sa242 struct{}
	sa243 struct{}
	sa244 struct{}
	sa245 struct{}
	sa246 struct{}
	sa247 struct{}
	sa248 struct{}
	sa249 struct{}
	sa250 struct{}
	sa251 struct{}
	sa252 struct{}
	sa253 struct{}
	sa254 struct{}
	sa255 struct{}
	sa256 struct{}
)

type (
	sb001 struct{}
	sb002 struct{}
	sb003 struct{}
	sb004 struct{}
	sb005 struct{}
	sb006 struct{}
	sb007 struct{}
	sb008 struct{}
	sb009 struct{}
	sb010 struct{}
	sb011 struct{}
	sb012 struct{}
	sb013 struct{}
	sb014 struct{}
	sb015 struct{}
	sb016 struct{}
	sb017 struct{}
	sb018 struct{}
	sb019 struct{}
	sb020 struct{}
	sb021 struct{}
	sb022 struct{}
	sb023 struct{}
	sb024 struct{}
	sb025 struct{}
	sb026 struct{}
	sb027 struct{}
	sb028 struct{}
	sb029 struct{}
	sb030 struct{}
	sb031 struct{}
	sb032 struct{}
	sb033 struct{}
	sb034 struct{}
	sb035 struct{}
	sb036 struct{}
	sb037 struct{}
	sb038 struct{}
	sb039 struct{}
	sb040 struct{}
	sb041 struct{}
	sb042 struct{}
	sb043 struct{}
	sb044 struct{}
	sb045 struct{}
	sb046 struct{}
	sb047 struct{}
	sb048 struct{}
	sb049 struct{}
	sb050 struct{}
	sb051 struct{}
	sb052 struct{}
	sb053 struct{}
	sb054 struct{}
	sb055 struct{}
	sb056 struct{}
	sb057 struct{}
	sb058 struct{}
	sb059 struct{}
	sb060 struct{}
	sb061 struct{}
	sb062 struct{}
	sb063 struct{}
	sb064 struct{}
	sb065 struct{}
	sb066 struct{}
	sb067 struct{}
	sb068 struct{}
	sb069 struct{}
	sb070 struct{}
	sb071 struct{}
	sb072 struct{}
	sb073 struct{}
	sb074 struct{}
	sb075 struct{}
	sb076 struct{}
	sb077 struct{}
	sb078 struct{}
	sb079 struct{}
	sb080 struct{}
	sb081 struct{}
	sb082 struct{}
	sb083 struct{}
	sb084 struct{}
	sb085 struct{}
	sb086 struct{}
	sb087 struct{}
	sb088 struct{}
	sb089 struct{}
	sb090 struct{}
	sb091 struct{}
	sb092 struct{}
	sb093 struct{}
	sb094 struct{}
	sb095 struct{}
	sb096 struct{}
	sb097 struct{}
	sb098 struct{}
	sb099 struct{}
	sb100 struct{}
	sb101 struct{}
	sb102 struct{}
	sb103 struct{}
	sb104 struct{}
	sb105 struct{}
	sb106 struct{}
	sb107 struct{}
	sb108 struct{}
	sb109 struct{}
	sb110 struct{}
	sb111 struct{}
	sb112 struct{}
	sb113 struct{}
	sb114 struct{}
	sb115 struct{}
	sb116 struct{}
	sb117 struct{}
	sb118 struct{}
	sb119 struct{}
	sb120 struct{}
	sb121 struct{}
	sb122 struct{}
	sb123 struct{}
	sb124 struct{}
	sb125 struct{}
	sb126 struct{}
	sb127 struct{}
	sb128 struct{}
	sb129 struct{}
	sb130 struct{}
	sb131 struct{}
	sb132 struct{}
	sb133 struct{}
	sb134 struct{}
	sb135 struct{}
	sb136 struct{}
	sb137 struct{}
	sb138 struct{}
	sb139 struct{}
	sb140 struct{}
	sb141 struct{}
	sb142 struct{}
	sb143 struct{}
	sb144 struct{}
	sb145 struct{}
	sb146 struct{}
	sb147 struct{}
	sb148 struct{}
	sb149 struct{}
	sb150 struct{}
	sb151 struct{}
	sb152 struct{}
	sb153 struct{}
	sb154 struct{}
	sb155 struct{}
	sb156 struct{}
	sb157 struct{}
	sb158 struct{}
	sb159 struct{}
	sb160 struct{}
	sb161 struct{}
	sb162 struct{}
	sb163 struct{}
	sb164 struct{}
	sb165 struct{}
	sb166 struct{}
	sb167 struct{}
	sb168 struct{}
	sb169 struct{}
	sb170 struct{}
	sb171 struct{}
	sb172 struct{}
	sb173 struct{}
	sb174 struct{}
	sb175 struct{}
	sb176 struct{}
	sb177 struct{}
	sb178 struct{}
	sb179 struct{}
	sb180 struct{}
	sb181 struct{}
	sb182 struct{}
	sb183 struct{}
	sb184 struct{}
	sb185 struct{}
	sb186 struct{}
	sb187 struct{}
	sb188 struct{}
	sb189 struct{}
	sb190 struct{}
	sb191 struct{}
	sb192 struct{}
	sb193 struct{}
	sb194 struct{}
	sb195 struct{}
	sb196 struct{}
	sb197 struct{}
	sb198 struct{}
	sb199 struct{}
	sb200 struct{}
	sb201 struct{}
	sb202 struct{}
	sb203 struct{}
	sb204 struct{}
	sb205 struct{}
	sb206 struct{}
	sb207 struct{}
	sb208 struct{}
	sb209 struct{}
	sb210 struct{}
	sb211 struct{}
	sb212 struct{}
	sb213 struct{}
	sb214 struct{}
	sb215 struct{}
	sb216 struct{}
	sb217 struct{}
	sb218 struct{}
	sb219 struct{}
	sb220 struct{}
	sb221 struct{}
	sb222 struct{}
	sb223 struct{}
	sb224 struct{}
	sb225 struct{}
	sb226 struct{}
	sb227 struct{}
	sb228 struct{}
	sb229 struct{}
	sb230 struct{}
	sb231 struct{}
	sb232 struct{}
	sb233 struct{}
	sb234 struct{}
	sb235 struct{}
	sb236 struct{}
	sb237 struct{}
	sb238 struct{}
	sb239 struct{}
	sb240 struct{}
	sb241 struct{}
	sb242 struct{}
	sb243 struct{}
	sb244 struct{}
	sb245 struct{}
	sb246 struct{}
	sb247 struct{}
	sb248 struct{}
	sb249 struct{}
	sb250 struct{}
	sb251 struct{}
	sb252 struct{}
	sb253 struct{}
	sb254 struct{}
	sb255 struct{}
	sb256 struct{}
	sb257 struct{}
	sb258 struct{}
)

func registerSA(c *gas.ServiceContainer) {
	c.RegisterTransientService[*sa001](func(*sa002) *sa001 { return &sa001{} })
	c.RegisterTransientService[*sa002](func(*sa003) *sa002 { return &sa002{} })
	c.RegisterTransientService[*sa003](func(*sa004) *sa003 { return &sa003{} })
	c.RegisterTransientService[*sa004](func(*sa005) *sa004 { return &sa004{} })
	c.RegisterTransientService[*sa005](func(*sa006) *sa005 { return &sa005{} })
	c.RegisterTransientService[*sa006](func(*sa007) *sa006 { return &sa006{} })
	c.RegisterTransientService[*sa007](func(*sa008) *sa007 { return &sa007{} })
	c.RegisterTransientService[*sa008](func(*sa009) *sa008 { return &sa008{} })
	c.RegisterTransientService[*sa009](func(*sa010) *sa009 { return &sa009{} })
	c.RegisterTransientService[*sa010](func(*sa011) *sa010 { return &sa010{} })
	c.RegisterTransientService[*sa011](func(*sa012) *sa011 { return &sa011{} })
	c.RegisterTransientService[*sa012](func(*sa013) *sa012 { return &sa012{} })
	c.RegisterTransientService[*sa013](func(*sa014) *sa013 { return &sa013{} })
	c.RegisterTransientService[*sa014](func(*sa015) *sa014 { return &sa014{} })
	c.RegisterTransientService[*sa015](func(*sa016) *sa015 { return &sa015{} })
	c.RegisterTransientService[*sa016](func(*sa017) *sa016 { return &sa016{} })
	c.RegisterTransientService[*sa017](func(*sa018) *sa017 { return &sa017{} })
	c.RegisterTransientService[*sa018](func(*sa019) *sa018 { return &sa018{} })
	c.RegisterTransientService[*sa019](func(*sa020) *sa019 { return &sa019{} })
	c.RegisterTransientService[*sa020](func(*sa021) *sa020 { return &sa020{} })
	c.RegisterTransientService[*sa021](func(*sa022) *sa021 { return &sa021{} })
	c.RegisterTransientService[*sa022](func(*sa023) *sa022 { return &sa022{} })
	c.RegisterTransientService[*sa023](func(*sa024) *sa023 { return &sa023{} })
	c.RegisterTransientService[*sa024](func(*sa025) *sa024 { return &sa024{} })
	c.RegisterTransientService[*sa025](func(*sa026) *sa025 { return &sa025{} })
	c.RegisterTransientService[*sa026](func(*sa027) *sa026 { return &sa026{} })
	c.RegisterTransientService[*sa027](func(*sa028) *sa027 { return &sa027{} })
	c.RegisterTransientService[*sa028](func(*sa029) *sa028 { return &sa028{} })
	c.RegisterTransientService[*sa029](func(*sa030) *sa029 { return &sa029{} })
	c.RegisterTransientService[*sa030](func(*sa031) *sa030 { return &sa030{} })
	c.RegisterTransientService[*sa031](func(*sa032) *sa031 { return &sa031{} })
	c.RegisterTransientService[*sa032](func(*sa033) *sa032 { return &sa032{} })
	c.RegisterTransientService[*sa033](func(*sa034) *sa033 { return &sa033{} })
	c.RegisterTransientService[*sa034](func(*sa035) *sa034 { return &sa034{} })
	c.RegisterTransientService[*sa035](func(*sa036) *sa035 { return &sa035{} })
	c.RegisterTransientService[*sa036](func(*sa037) *sa036 { return &sa036{} })
	c.RegisterTransientService[*sa037](func(*sa038) *sa037 { return &sa037{} })
	c.RegisterTransientService[*sa038](func(*sa039) *sa038 { return &sa038{} })
	c.RegisterTransientService[*sa039](func(*sa040) *sa039 { return &sa039{} })
	c.RegisterTransientService[*sa040](func(*sa041) *sa040 { return &sa040{} })
	c.RegisterTransientService[*sa041](func(*sa042) *sa041 { return &sa041{} })
	c.RegisterTransientService[*sa042](func(*sa043) *sa042 { return &sa042{} })
	c.RegisterTransientService[*sa043](func(*sa044) *sa043 { return &sa043{} })
	c.RegisterTransientService[*sa044](func(*sa045) *sa044 { return &sa044{} })
	c.RegisterTransientService[*sa045](func(*sa046) *sa045 { return &sa045{} })
	c.RegisterTransientService[*sa046](func(*sa047) *sa046 { return &sa046{} })
	c.RegisterTransientService[*sa047](func(*sa048) *sa047 { return &sa047{} })
	c.RegisterTransientService[*sa048](func(*sa049) *sa048 { return &sa048{} })
	c.RegisterTransientService[*sa049](func(*sa050) *sa049 { return &sa049{} })
	c.RegisterTransientService[*sa050](func(*sa051) *sa050 { return &sa050{} })
	c.RegisterTransientService[*sa051](func(*sa052) *sa051 { return &sa051{} })
	c.RegisterTransientService[*sa052](func(*sa053) *sa052 { return &sa052{} })
	c.RegisterTransientService[*sa053](func(*sa054) *sa053 { return &sa053{} })
	c.RegisterTransientService[*sa054](func(*sa055) *sa054 { return &sa054{} })
	c.RegisterTransientService[*sa055](func(*sa056) *sa055 { return &sa055{} })
	c.RegisterTransientService[*sa056](func(*sa057) *sa056 { return &sa056{} })
	c.RegisterTransientService[*sa057](func(*sa058) *sa057 { return &sa057{} })
	c.RegisterTransientService[*sa058](func(*sa059) *sa058 { return &sa058{} })
	c.RegisterTransientService[*sa059](func(*sa060) *sa059 { return &sa059{} })
	c.RegisterTransientService[*sa060](func(*sa061) *sa060 { return &sa060{} })
	c.RegisterTransientService[*sa061](func(*sa062) *sa061 { return &sa061{} })
	c.RegisterTransientService[*sa062](func(*sa063) *sa062 { return &sa062{} })
	c.RegisterTransientService[*sa063](func(*sa064) *sa063 { return &sa063{} })
	c.RegisterTransientService[*sa064](func(*sa065) *sa064 { return &sa064{} })
	c.RegisterTransientService[*sa065](func(*sa066) *sa065 { return &sa065{} })
	c.RegisterTransientService[*sa066](func(*sa067) *sa066 { return &sa066{} })
	c.RegisterTransientService[*sa067](func(*sa068) *sa067 { return &sa067{} })
	c.RegisterTransientService[*sa068](func(*sa069) *sa068 { return &sa068{} })
	c.RegisterTransientService[*sa069](func(*sa070) *sa069 { return &sa069{} })
	c.RegisterTransientService[*sa070](func(*sa071) *sa070 { return &sa070{} })
	c.RegisterTransientService[*sa071](func(*sa072) *sa071 { return &sa071{} })
	c.RegisterTransientService[*sa072](func(*sa073) *sa072 { return &sa072{} })
	c.RegisterTransientService[*sa073](func(*sa074) *sa073 { return &sa073{} })
	c.RegisterTransientService[*sa074](func(*sa075) *sa074 { return &sa074{} })
	c.RegisterTransientService[*sa075](func(*sa076) *sa075 { return &sa075{} })
	c.RegisterTransientService[*sa076](func(*sa077) *sa076 { return &sa076{} })
	c.RegisterTransientService[*sa077](func(*sa078) *sa077 { return &sa077{} })
	c.RegisterTransientService[*sa078](func(*sa079) *sa078 { return &sa078{} })
	c.RegisterTransientService[*sa079](func(*sa080) *sa079 { return &sa079{} })
	c.RegisterTransientService[*sa080](func(*sa081) *sa080 { return &sa080{} })
	c.RegisterTransientService[*sa081](func(*sa082) *sa081 { return &sa081{} })
	c.RegisterTransientService[*sa082](func(*sa083) *sa082 { return &sa082{} })
	c.RegisterTransientService[*sa083](func(*sa084) *sa083 { return &sa083{} })
	c.RegisterTransientService[*sa084](func(*sa085) *sa084 { return &sa084{} })
	c.RegisterTransientService[*sa085](func(*sa086) *sa085 { return &sa085{} })
	c.RegisterTransientService[*sa086](func(*sa087) *sa086 { return &sa086{} })
	c.RegisterTransientService[*sa087](func(*sa088) *sa087 { return &sa087{} })
	c.RegisterTransientService[*sa088](func(*sa089) *sa088 { return &sa088{} })
	c.RegisterTransientService[*sa089](func(*sa090) *sa089 { return &sa089{} })
	c.RegisterTransientService[*sa090](func(*sa091) *sa090 { return &sa090{} })
	c.RegisterTransientService[*sa091](func(*sa092) *sa091 { return &sa091{} })
	c.RegisterTransientService[*sa092](func(*sa093) *sa092 { return &sa092{} })
	c.RegisterTransientService[*sa093](func(*sa094) *sa093 { return &sa093{} })
	c.RegisterTransientService[*sa094](func(*sa095) *sa094 { return &sa094{} })
	c.RegisterTransientService[*sa095](func(*sa096) *sa095 { return &sa095{} })
	c.RegisterTransientService[*sa096](func(*sa097) *sa096 { return &sa096{} })
	c.RegisterTransientService[*sa097](func(*sa098) *sa097 { return &sa097{} })
	c.RegisterTransientService[*sa098](func(*sa099) *sa098 { return &sa098{} })
	c.RegisterTransientService[*sa099](func(*sa100) *sa099 { return &sa099{} })
	c.RegisterTransientService[*sa100](func(*sa101) *sa100 { return &sa100{} })
	c.RegisterTransientService[*sa101](func(*sa102) *sa101 { return &sa101{} })
	c.RegisterTransientService[*sa102](func(*sa103) *sa102 { return &sa102{} })
	c.RegisterTransientService[*sa103](func(*sa104) *sa103 { return &sa103{} })
	c.RegisterTransientService[*sa104](func(*sa105) *sa104 { return &sa104{} })
	c.RegisterTransientService[*sa105](func(*sa106) *sa105 { return &sa105{} })
	c.RegisterTransientService[*sa106](func(*sa107) *sa106 { return &sa106{} })
	c.RegisterTransientService[*sa107](func(*sa108) *sa107 { return &sa107{} })
	c.RegisterTransientService[*sa108](func(*sa109) *sa108 { return &sa108{} })
	c.RegisterTransientService[*sa109](func(*sa110) *sa109 { return &sa109{} })
	c.RegisterTransientService[*sa110](func(*sa111) *sa110 { return &sa110{} })
	c.RegisterTransientService[*sa111](func(*sa112) *sa111 { return &sa111{} })
	c.RegisterTransientService[*sa112](func(*sa113) *sa112 { return &sa112{} })
	c.RegisterTransientService[*sa113](func(*sa114) *sa113 { return &sa113{} })
	c.RegisterTransientService[*sa114](func(*sa115) *sa114 { return &sa114{} })
	c.RegisterTransientService[*sa115](func(*sa116) *sa115 { return &sa115{} })
	c.RegisterTransientService[*sa116](func(*sa117) *sa116 { return &sa116{} })
	c.RegisterTransientService[*sa117](func(*sa118) *sa117 { return &sa117{} })
	c.RegisterTransientService[*sa118](func(*sa119) *sa118 { return &sa118{} })
	c.RegisterTransientService[*sa119](func(*sa120) *sa119 { return &sa119{} })
	c.RegisterTransientService[*sa120](func(*sa121) *sa120 { return &sa120{} })
	c.RegisterTransientService[*sa121](func(*sa122) *sa121 { return &sa121{} })
	c.RegisterTransientService[*sa122](func(*sa123) *sa122 { return &sa122{} })
	c.RegisterTransientService[*sa123](func(*sa124) *sa123 { return &sa123{} })
	c.RegisterTransientService[*sa124](func(*sa125) *sa124 { return &sa124{} })
	c.RegisterTransientService[*sa125](func(*sa126) *sa125 { return &sa125{} })
	c.RegisterTransientService[*sa126](func(*sa127) *sa126 { return &sa126{} })
	c.RegisterTransientService[*sa127](func(*sa128) *sa127 { return &sa127{} })
	c.RegisterTransientService[*sa128](func(*sa129) *sa128 { return &sa128{} })
	c.RegisterTransientService[*sa129](func(*sa130) *sa129 { return &sa129{} })
	c.RegisterTransientService[*sa130](func(*sa131) *sa130 { return &sa130{} })
	c.RegisterTransientService[*sa131](func(*sa132) *sa131 { return &sa131{} })
	c.RegisterTransientService[*sa132](func(*sa133) *sa132 { return &sa132{} })
	c.RegisterTransientService[*sa133](func(*sa134) *sa133 { return &sa133{} })
	c.RegisterTransientService[*sa134](func(*sa135) *sa134 { return &sa134{} })
	c.RegisterTransientService[*sa135](func(*sa136) *sa135 { return &sa135{} })
	c.RegisterTransientService[*sa136](func(*sa137) *sa136 { return &sa136{} })
	c.RegisterTransientService[*sa137](func(*sa138) *sa137 { return &sa137{} })
	c.RegisterTransientService[*sa138](func(*sa139) *sa138 { return &sa138{} })
	c.RegisterTransientService[*sa139](func(*sa140) *sa139 { return &sa139{} })
	c.RegisterTransientService[*sa140](func(*sa141) *sa140 { return &sa140{} })
	c.RegisterTransientService[*sa141](func(*sa142) *sa141 { return &sa141{} })
	c.RegisterTransientService[*sa142](func(*sa143) *sa142 { return &sa142{} })
	c.RegisterTransientService[*sa143](func(*sa144) *sa143 { return &sa143{} })
	c.RegisterTransientService[*sa144](func(*sa145) *sa144 { return &sa144{} })
	c.RegisterTransientService[*sa145](func(*sa146) *sa145 { return &sa145{} })
	c.RegisterTransientService[*sa146](func(*sa147) *sa146 { return &sa146{} })
	c.RegisterTransientService[*sa147](func(*sa148) *sa147 { return &sa147{} })
	c.RegisterTransientService[*sa148](func(*sa149) *sa148 { return &sa148{} })
	c.RegisterTransientService[*sa149](func(*sa150) *sa149 { return &sa149{} })
	c.RegisterTransientService[*sa150](func(*sa151) *sa150 { return &sa150{} })
	c.RegisterTransientService[*sa151](func(*sa152) *sa151 { return &sa151{} })
	c.RegisterTransientService[*sa152](func(*sa153) *sa152 { return &sa152{} })
	c.RegisterTransientService[*sa153](func(*sa154) *sa153 { return &sa153{} })
	c.RegisterTransientService[*sa154](func(*sa155) *sa154 { return &sa154{} })
	c.RegisterTransientService[*sa155](func(*sa156) *sa155 { return &sa155{} })
	c.RegisterTransientService[*sa156](func(*sa157) *sa156 { return &sa156{} })
	c.RegisterTransientService[*sa157](func(*sa158) *sa157 { return &sa157{} })
	c.RegisterTransientService[*sa158](func(*sa159) *sa158 { return &sa158{} })
	c.RegisterTransientService[*sa159](func(*sa160) *sa159 { return &sa159{} })
	c.RegisterTransientService[*sa160](func(*sa161) *sa160 { return &sa160{} })
	c.RegisterTransientService[*sa161](func(*sa162) *sa161 { return &sa161{} })
	c.RegisterTransientService[*sa162](func(*sa163) *sa162 { return &sa162{} })
	c.RegisterTransientService[*sa163](func(*sa164) *sa163 { return &sa163{} })
	c.RegisterTransientService[*sa164](func(*sa165) *sa164 { return &sa164{} })
	c.RegisterTransientService[*sa165](func(*sa166) *sa165 { return &sa165{} })
	c.RegisterTransientService[*sa166](func(*sa167) *sa166 { return &sa166{} })
	c.RegisterTransientService[*sa167](func(*sa168) *sa167 { return &sa167{} })
	c.RegisterTransientService[*sa168](func(*sa169) *sa168 { return &sa168{} })
	c.RegisterTransientService[*sa169](func(*sa170) *sa169 { return &sa169{} })
	c.RegisterTransientService[*sa170](func(*sa171) *sa170 { return &sa170{} })
	c.RegisterTransientService[*sa171](func(*sa172) *sa171 { return &sa171{} })
	c.RegisterTransientService[*sa172](func(*sa173) *sa172 { return &sa172{} })
	c.RegisterTransientService[*sa173](func(*sa174) *sa173 { return &sa173{} })
	c.RegisterTransientService[*sa174](func(*sa175) *sa174 { return &sa174{} })
	c.RegisterTransientService[*sa175](func(*sa176) *sa175 { return &sa175{} })
	c.RegisterTransientService[*sa176](func(*sa177) *sa176 { return &sa176{} })
	c.RegisterTransientService[*sa177](func(*sa178) *sa177 { return &sa177{} })
	c.RegisterTransientService[*sa178](func(*sa179) *sa178 { return &sa178{} })
	c.RegisterTransientService[*sa179](func(*sa180) *sa179 { return &sa179{} })
	c.RegisterTransientService[*sa180](func(*sa181) *sa180 { return &sa180{} })
	c.RegisterTransientService[*sa181](func(*sa182) *sa181 { return &sa181{} })
	c.RegisterTransientService[*sa182](func(*sa183) *sa182 { return &sa182{} })
	c.RegisterTransientService[*sa183](func(*sa184) *sa183 { return &sa183{} })
	c.RegisterTransientService[*sa184](func(*sa185) *sa184 { return &sa184{} })
	c.RegisterTransientService[*sa185](func(*sa186) *sa185 { return &sa185{} })
	c.RegisterTransientService[*sa186](func(*sa187) *sa186 { return &sa186{} })
	c.RegisterTransientService[*sa187](func(*sa188) *sa187 { return &sa187{} })
	c.RegisterTransientService[*sa188](func(*sa189) *sa188 { return &sa188{} })
	c.RegisterTransientService[*sa189](func(*sa190) *sa189 { return &sa189{} })
	c.RegisterTransientService[*sa190](func(*sa191) *sa190 { return &sa190{} })
	c.RegisterTransientService[*sa191](func(*sa192) *sa191 { return &sa191{} })
	c.RegisterTransientService[*sa192](func(*sa193) *sa192 { return &sa192{} })
	c.RegisterTransientService[*sa193](func(*sa194) *sa193 { return &sa193{} })
	c.RegisterTransientService[*sa194](func(*sa195) *sa194 { return &sa194{} })
	c.RegisterTransientService[*sa195](func(*sa196) *sa195 { return &sa195{} })
	c.RegisterTransientService[*sa196](func(*sa197) *sa196 { return &sa196{} })
	c.RegisterTransientService[*sa197](func(*sa198) *sa197 { return &sa197{} })
	c.RegisterTransientService[*sa198](func(*sa199) *sa198 { return &sa198{} })
	c.RegisterTransientService[*sa199](func(*sa200) *sa199 { return &sa199{} })
	c.RegisterTransientService[*sa200](func(*sa201) *sa200 { return &sa200{} })
	c.RegisterTransientService[*sa201](func(*sa202) *sa201 { return &sa201{} })
	c.RegisterTransientService[*sa202](func(*sa203) *sa202 { return &sa202{} })
	c.RegisterTransientService[*sa203](func(*sa204) *sa203 { return &sa203{} })
	c.RegisterTransientService[*sa204](func(*sa205) *sa204 { return &sa204{} })
	c.RegisterTransientService[*sa205](func(*sa206) *sa205 { return &sa205{} })
	c.RegisterTransientService[*sa206](func(*sa207) *sa206 { return &sa206{} })
	c.RegisterTransientService[*sa207](func(*sa208) *sa207 { return &sa207{} })
	c.RegisterTransientService[*sa208](func(*sa209) *sa208 { return &sa208{} })
	c.RegisterTransientService[*sa209](func(*sa210) *sa209 { return &sa209{} })
	c.RegisterTransientService[*sa210](func(*sa211) *sa210 { return &sa210{} })
	c.RegisterTransientService[*sa211](func(*sa212) *sa211 { return &sa211{} })
	c.RegisterTransientService[*sa212](func(*sa213) *sa212 { return &sa212{} })
	c.RegisterTransientService[*sa213](func(*sa214) *sa213 { return &sa213{} })
	c.RegisterTransientService[*sa214](func(*sa215) *sa214 { return &sa214{} })
	c.RegisterTransientService[*sa215](func(*sa216) *sa215 { return &sa215{} })
	c.RegisterTransientService[*sa216](func(*sa217) *sa216 { return &sa216{} })
	c.RegisterTransientService[*sa217](func(*sa218) *sa217 { return &sa217{} })
	c.RegisterTransientService[*sa218](func(*sa219) *sa218 { return &sa218{} })
	c.RegisterTransientService[*sa219](func(*sa220) *sa219 { return &sa219{} })
	c.RegisterTransientService[*sa220](func(*sa221) *sa220 { return &sa220{} })
	c.RegisterTransientService[*sa221](func(*sa222) *sa221 { return &sa221{} })
	c.RegisterTransientService[*sa222](func(*sa223) *sa222 { return &sa222{} })
	c.RegisterTransientService[*sa223](func(*sa224) *sa223 { return &sa223{} })
	c.RegisterTransientService[*sa224](func(*sa225) *sa224 { return &sa224{} })
	c.RegisterTransientService[*sa225](func(*sa226) *sa225 { return &sa225{} })
	c.RegisterTransientService[*sa226](func(*sa227) *sa226 { return &sa226{} })
	c.RegisterTransientService[*sa227](func(*sa228) *sa227 { return &sa227{} })
	c.RegisterTransientService[*sa228](func(*sa229) *sa228 { return &sa228{} })
	c.RegisterTransientService[*sa229](func(*sa230) *sa229 { return &sa229{} })
	c.RegisterTransientService[*sa230](func(*sa231) *sa230 { return &sa230{} })
	c.RegisterTransientService[*sa231](func(*sa232) *sa231 { return &sa231{} })
	c.RegisterTransientService[*sa232](func(*sa233) *sa232 { return &sa232{} })
	c.RegisterTransientService[*sa233](func(*sa234) *sa233 { return &sa233{} })
	c.RegisterTransientService[*sa234](func(*sa235) *sa234 { return &sa234{} })
	c.RegisterTransientService[*sa235](func(*sa236) *sa235 { return &sa235{} })
	c.RegisterTransientService[*sa236](func(*sa237) *sa236 { return &sa236{} })
	c.RegisterTransientService[*sa237](func(*sa238) *sa237 { return &sa237{} })
	c.RegisterTransientService[*sa238](func(*sa239) *sa238 { return &sa238{} })
	c.RegisterTransientService[*sa239](func(*sa240) *sa239 { return &sa239{} })
	c.RegisterTransientService[*sa240](func(*sa241) *sa240 { return &sa240{} })
	c.RegisterTransientService[*sa241](func(*sa242) *sa241 { return &sa241{} })
	c.RegisterTransientService[*sa242](func(*sa243) *sa242 { return &sa242{} })
	c.RegisterTransientService[*sa243](func(*sa244) *sa243 { return &sa243{} })
	c.RegisterTransientService[*sa244](func(*sa245) *sa244 { return &sa244{} })
	c.RegisterTransientService[*sa245](func(*sa246) *sa245 { return &sa245{} })
	c.RegisterTransientService[*sa246](func(*sa247) *sa246 { return &sa246{} })
	c.RegisterTransientService[*sa247](func(*sa248) *sa247 { return &sa247{} })
	c.RegisterTransientService[*sa248](func(*sa249) *sa248 { return &sa248{} })
	c.RegisterTransientService[*sa249](func(*sa250) *sa249 { return &sa249{} })
	c.RegisterTransientService[*sa250](func(*sa251) *sa250 { return &sa250{} })
	c.RegisterTransientService[*sa251](func(*sa252) *sa251 { return &sa251{} })
	c.RegisterTransientService[*sa252](func(*sa253) *sa252 { return &sa252{} })
	c.RegisterTransientService[*sa253](func(*sa254) *sa253 { return &sa253{} })
	c.RegisterTransientService[*sa254](func(*sa255) *sa254 { return &sa254{} })
	c.RegisterTransientService[*sa255](func(*sa256) *sa255 { return &sa255{} })
	c.RegisterTransientService[*sa256](func() *sa256 { return &sa256{} })
}

func registerSB(c *gas.ServiceContainer) {
	c.RegisterTransientService[*sb001](func(*sb002) *sb001 { return &sb001{} })
	c.RegisterTransientService[*sb002](func(*sb003) *sb002 { return &sb002{} })
	c.RegisterTransientService[*sb003](func(*sb004) *sb003 { return &sb003{} })
	c.RegisterTransientService[*sb004](func(*sb005) *sb004 { return &sb004{} })
	c.RegisterTransientService[*sb005](func(*sb006) *sb005 { return &sb005{} })
	c.RegisterTransientService[*sb006](func(*sb007) *sb006 { return &sb006{} })
	c.RegisterTransientService[*sb007](func(*sb008) *sb007 { return &sb007{} })
	c.RegisterTransientService[*sb008](func(*sb009) *sb008 { return &sb008{} })
	c.RegisterTransientService[*sb009](func(*sb010) *sb009 { return &sb009{} })
	c.RegisterTransientService[*sb010](func(*sb011) *sb010 { return &sb010{} })
	c.RegisterTransientService[*sb011](func(*sb012) *sb011 { return &sb011{} })
	c.RegisterTransientService[*sb012](func(*sb013) *sb012 { return &sb012{} })
	c.RegisterTransientService[*sb013](func(*sb014) *sb013 { return &sb013{} })
	c.RegisterTransientService[*sb014](func(*sb015) *sb014 { return &sb014{} })
	c.RegisterTransientService[*sb015](func(*sb016) *sb015 { return &sb015{} })
	c.RegisterTransientService[*sb016](func(*sb017) *sb016 { return &sb016{} })
	c.RegisterTransientService[*sb017](func(*sb018) *sb017 { return &sb017{} })
	c.RegisterTransientService[*sb018](func(*sb019) *sb018 { return &sb018{} })
	c.RegisterTransientService[*sb019](func(*sb020) *sb019 { return &sb019{} })
	c.RegisterTransientService[*sb020](func(*sb021) *sb020 { return &sb020{} })
	c.RegisterTransientService[*sb021](func(*sb022) *sb021 { return &sb021{} })
	c.RegisterTransientService[*sb022](func(*sb023) *sb022 { return &sb022{} })
	c.RegisterTransientService[*sb023](func(*sb024) *sb023 { return &sb023{} })
	c.RegisterTransientService[*sb024](func(*sb025) *sb024 { return &sb024{} })
	c.RegisterTransientService[*sb025](func(*sb026) *sb025 { return &sb025{} })
	c.RegisterTransientService[*sb026](func(*sb027) *sb026 { return &sb026{} })
	c.RegisterTransientService[*sb027](func(*sb028) *sb027 { return &sb027{} })
	c.RegisterTransientService[*sb028](func(*sb029) *sb028 { return &sb028{} })
	c.RegisterTransientService[*sb029](func(*sb030) *sb029 { return &sb029{} })
	c.RegisterTransientService[*sb030](func(*sb031) *sb030 { return &sb030{} })
	c.RegisterTransientService[*sb031](func(*sb032) *sb031 { return &sb031{} })
	c.RegisterTransientService[*sb032](func(*sb033) *sb032 { return &sb032{} })
	c.RegisterTransientService[*sb033](func(*sb034) *sb033 { return &sb033{} })
	c.RegisterTransientService[*sb034](func(*sb035) *sb034 { return &sb034{} })
	c.RegisterTransientService[*sb035](func(*sb036) *sb035 { return &sb035{} })
	c.RegisterTransientService[*sb036](func(*sb037) *sb036 { return &sb036{} })
	c.RegisterTransientService[*sb037](func(*sb038) *sb037 { return &sb037{} })
	c.RegisterTransientService[*sb038](func(*sb039) *sb038 { return &sb038{} })
	c.RegisterTransientService[*sb039](func(*sb040) *sb039 { return &sb039{} })
	c.RegisterTransientService[*sb040](func(*sb041) *sb040 { return &sb040{} })
	c.RegisterTransientService[*sb041](func(*sb042) *sb041 { return &sb041{} })
	c.RegisterTransientService[*sb042](func(*sb043) *sb042 { return &sb042{} })
	c.RegisterTransientService[*sb043](func(*sb044) *sb043 { return &sb043{} })
	c.RegisterTransientService[*sb044](func(*sb045) *sb044 { return &sb044{} })
	c.RegisterTransientService[*sb045](func(*sb046) *sb045 { return &sb045{} })
	c.RegisterTransientService[*sb046](func(*sb047) *sb046 { return &sb046{} })
	c.RegisterTransientService[*sb047](func(*sb048) *sb047 { return &sb047{} })
	c.RegisterTransientService[*sb048](func(*sb049) *sb048 { return &sb048{} })
	c.RegisterTransientService[*sb049](func(*sb050) *sb049 { return &sb049{} })
	c.RegisterTransientService[*sb050](func(*sb051) *sb050 { return &sb050{} })
	c.RegisterTransientService[*sb051](func(*sb052) *sb051 { return &sb051{} })
	c.RegisterTransientService[*sb052](func(*sb053) *sb052 { return &sb052{} })
	c.RegisterTransientService[*sb053](func(*sb054) *sb053 { return &sb053{} })
	c.RegisterTransientService[*sb054](func(*sb055) *sb054 { return &sb054{} })
	c.RegisterTransientService[*sb055](func(*sb056) *sb055 { return &sb055{} })
	c.RegisterTransientService[*sb056](func(*sb057) *sb056 { return &sb056{} })
	c.RegisterTransientService[*sb057](func(*sb058) *sb057 { return &sb057{} })
	c.RegisterTransientService[*sb058](func(*sb059) *sb058 { return &sb058{} })
	c.RegisterTransientService[*sb059](func(*sb060) *sb059 { return &sb059{} })
	c.RegisterTransientService[*sb060](func(*sb061) *sb060 { return &sb060{} })
	c.RegisterTransientService[*sb061](func(*sb062) *sb061 { return &sb061{} })
	c.RegisterTransientService[*sb062](func(*sb063) *sb062 { return &sb062{} })
	c.RegisterTransientService[*sb063](func(*sb064) *sb063 { return &sb063{} })
	c.RegisterTransientService[*sb064](func(*sb065) *sb064 { return &sb064{} })
	c.RegisterTransientService[*sb065](func(*sb066) *sb065 { return &sb065{} })
	c.RegisterTransientService[*sb066](func(*sb067) *sb066 { return &sb066{} })
	c.RegisterTransientService[*sb067](func(*sb068) *sb067 { return &sb067{} })
	c.RegisterTransientService[*sb068](func(*sb069) *sb068 { return &sb068{} })
	c.RegisterTransientService[*sb069](func(*sb070) *sb069 { return &sb069{} })
	c.RegisterTransientService[*sb070](func(*sb071) *sb070 { return &sb070{} })
	c.RegisterTransientService[*sb071](func(*sb072) *sb071 { return &sb071{} })
	c.RegisterTransientService[*sb072](func(*sb073) *sb072 { return &sb072{} })
	c.RegisterTransientService[*sb073](func(*sb074) *sb073 { return &sb073{} })
	c.RegisterTransientService[*sb074](func(*sb075) *sb074 { return &sb074{} })
	c.RegisterTransientService[*sb075](func(*sb076) *sb075 { return &sb075{} })
	c.RegisterTransientService[*sb076](func(*sb077) *sb076 { return &sb076{} })
	c.RegisterTransientService[*sb077](func(*sb078) *sb077 { return &sb077{} })
	c.RegisterTransientService[*sb078](func(*sb079) *sb078 { return &sb078{} })
	c.RegisterTransientService[*sb079](func(*sb080) *sb079 { return &sb079{} })
	c.RegisterTransientService[*sb080](func(*sb081) *sb080 { return &sb080{} })
	c.RegisterTransientService[*sb081](func(*sb082) *sb081 { return &sb081{} })
	c.RegisterTransientService[*sb082](func(*sb083) *sb082 { return &sb082{} })
	c.RegisterTransientService[*sb083](func(*sb084) *sb083 { return &sb083{} })
	c.RegisterTransientService[*sb084](func(*sb085) *sb084 { return &sb084{} })
	c.RegisterTransientService[*sb085](func(*sb086) *sb085 { return &sb085{} })
	c.RegisterTransientService[*sb086](func(*sb087) *sb086 { return &sb086{} })
	c.RegisterTransientService[*sb087](func(*sb088) *sb087 { return &sb087{} })
	c.RegisterTransientService[*sb088](func(*sb089) *sb088 { return &sb088{} })
	c.RegisterTransientService[*sb089](func(*sb090) *sb089 { return &sb089{} })
	c.RegisterTransientService[*sb090](func(*sb091) *sb090 { return &sb090{} })
	c.RegisterTransientService[*sb091](func(*sb092) *sb091 { return &sb091{} })
	c.RegisterTransientService[*sb092](func(*sb093) *sb092 { return &sb092{} })
	c.RegisterTransientService[*sb093](func(*sb094) *sb093 { return &sb093{} })
	c.RegisterTransientService[*sb094](func(*sb095) *sb094 { return &sb094{} })
	c.RegisterTransientService[*sb095](func(*sb096) *sb095 { return &sb095{} })
	c.RegisterTransientService[*sb096](func(*sb097) *sb096 { return &sb096{} })
	c.RegisterTransientService[*sb097](func(*sb098) *sb097 { return &sb097{} })
	c.RegisterTransientService[*sb098](func(*sb099) *sb098 { return &sb098{} })
	c.RegisterTransientService[*sb099](func(*sb100) *sb099 { return &sb099{} })
	c.RegisterTransientService[*sb100](func(*sb101) *sb100 { return &sb100{} })
	c.RegisterTransientService[*sb101](func(*sb102) *sb101 { return &sb101{} })
	c.RegisterTransientService[*sb102](func(*sb103) *sb102 { return &sb102{} })
	c.RegisterTransientService[*sb103](func(*sb104) *sb103 { return &sb103{} })
	c.RegisterTransientService[*sb104](func(*sb105) *sb104 { return &sb104{} })
	c.RegisterTransientService[*sb105](func(*sb106) *sb105 { return &sb105{} })
	c.RegisterTransientService[*sb106](func(*sb107) *sb106 { return &sb106{} })
	c.RegisterTransientService[*sb107](func(*sb108) *sb107 { return &sb107{} })
	c.RegisterTransientService[*sb108](func(*sb109) *sb108 { return &sb108{} })
	c.RegisterTransientService[*sb109](func(*sb110) *sb109 { return &sb109{} })
	c.RegisterTransientService[*sb110](func(*sb111) *sb110 { return &sb110{} })
	c.RegisterTransientService[*sb111](func(*sb112) *sb111 { return &sb111{} })
	c.RegisterTransientService[*sb112](func(*sb113) *sb112 { return &sb112{} })
	c.RegisterTransientService[*sb113](func(*sb114) *sb113 { return &sb113{} })
	c.RegisterTransientService[*sb114](func(*sb115) *sb114 { return &sb114{} })
	c.RegisterTransientService[*sb115](func(*sb116) *sb115 { return &sb115{} })
	c.RegisterTransientService[*sb116](func(*sb117) *sb116 { return &sb116{} })
	c.RegisterTransientService[*sb117](func(*sb118) *sb117 { return &sb117{} })
	c.RegisterTransientService[*sb118](func(*sb119) *sb118 { return &sb118{} })
	c.RegisterTransientService[*sb119](func(*sb120) *sb119 { return &sb119{} })
	c.RegisterTransientService[*sb120](func(*sb121) *sb120 { return &sb120{} })
	c.RegisterTransientService[*sb121](func(*sb122) *sb121 { return &sb121{} })
	c.RegisterTransientService[*sb122](func(*sb123) *sb122 { return &sb122{} })
	c.RegisterTransientService[*sb123](func(*sb124) *sb123 { return &sb123{} })
	c.RegisterTransientService[*sb124](func(*sb125) *sb124 { return &sb124{} })
	c.RegisterTransientService[*sb125](func(*sb126) *sb125 { return &sb125{} })
	c.RegisterTransientService[*sb126](func(*sb127) *sb126 { return &sb126{} })
	c.RegisterTransientService[*sb127](func(*sb128) *sb127 { return &sb127{} })
	c.RegisterTransientService[*sb128](func(*sb129) *sb128 { return &sb128{} })
	c.RegisterTransientService[*sb129](func(*sb130) *sb129 { return &sb129{} })
	c.RegisterTransientService[*sb130](func(*sb131) *sb130 { return &sb130{} })
	c.RegisterTransientService[*sb131](func(*sb132) *sb131 { return &sb131{} })
	c.RegisterTransientService[*sb132](func(*sb133) *sb132 { return &sb132{} })
	c.RegisterTransientService[*sb133](func(*sb134) *sb133 { return &sb133{} })
	c.RegisterTransientService[*sb134](func(*sb135) *sb134 { return &sb134{} })
	c.RegisterTransientService[*sb135](func(*sb136) *sb135 { return &sb135{} })
	c.RegisterTransientService[*sb136](func(*sb137) *sb136 { return &sb136{} })
	c.RegisterTransientService[*sb137](func(*sb138) *sb137 { return &sb137{} })
	c.RegisterTransientService[*sb138](func(*sb139) *sb138 { return &sb138{} })
	c.RegisterTransientService[*sb139](func(*sb140) *sb139 { return &sb139{} })
	c.RegisterTransientService[*sb140](func(*sb141) *sb140 { return &sb140{} })
	c.RegisterTransientService[*sb141](func(*sb142) *sb141 { return &sb141{} })
	c.RegisterTransientService[*sb142](func(*sb143) *sb142 { return &sb142{} })
	c.RegisterTransientService[*sb143](func(*sb144) *sb143 { return &sb143{} })
	c.RegisterTransientService[*sb144](func(*sb145) *sb144 { return &sb144{} })
	c.RegisterTransientService[*sb145](func(*sb146) *sb145 { return &sb145{} })
	c.RegisterTransientService[*sb146](func(*sb147) *sb146 { return &sb146{} })
	c.RegisterTransientService[*sb147](func(*sb148) *sb147 { return &sb147{} })
	c.RegisterTransientService[*sb148](func(*sb149) *sb148 { return &sb148{} })
	c.RegisterTransientService[*sb149](func(*sb150) *sb149 { return &sb149{} })
	c.RegisterTransientService[*sb150](func(*sb151) *sb150 { return &sb150{} })
	c.RegisterTransientService[*sb151](func(*sb152) *sb151 { return &sb151{} })
	c.RegisterTransientService[*sb152](func(*sb153) *sb152 { return &sb152{} })
	c.RegisterTransientService[*sb153](func(*sb154) *sb153 { return &sb153{} })
	c.RegisterTransientService[*sb154](func(*sb155) *sb154 { return &sb154{} })
	c.RegisterTransientService[*sb155](func(*sb156) *sb155 { return &sb155{} })
	c.RegisterTransientService[*sb156](func(*sb157) *sb156 { return &sb156{} })
	c.RegisterTransientService[*sb157](func(*sb158) *sb157 { return &sb157{} })
	c.RegisterTransientService[*sb158](func(*sb159) *sb158 { return &sb158{} })
	c.RegisterTransientService[*sb159](func(*sb160) *sb159 { return &sb159{} })
	c.RegisterTransientService[*sb160](func(*sb161) *sb160 { return &sb160{} })
	c.RegisterTransientService[*sb161](func(*sb162) *sb161 { return &sb161{} })
	c.RegisterTransientService[*sb162](func(*sb163) *sb162 { return &sb162{} })
	c.RegisterTransientService[*sb163](func(*sb164) *sb163 { return &sb163{} })
	c.RegisterTransientService[*sb164](func(*sb165) *sb164 { return &sb164{} })
	c.RegisterTransientService[*sb165](func(*sb166) *sb165 { return &sb165{} })
	c.RegisterTransientService[*sb166](func(*sb167) *sb166 { return &sb166{} })
	c.RegisterTransientService[*sb167](func(*sb168) *sb167 { return &sb167{} })
	c.RegisterTransientService[*sb168](func(*sb169) *sb168 { return &sb168{} })
	c.RegisterTransientService[*sb169](func(*sb170) *sb169 { return &sb169{} })
	c.RegisterTransientService[*sb170](func(*sb171) *sb170 { return &sb170{} })
	c.RegisterTransientService[*sb171](func(*sb172) *sb171 { return &sb171{} })
	c.RegisterTransientService[*sb172](func(*sb173) *sb172 { return &sb172{} })
	c.RegisterTransientService[*sb173](func(*sb174) *sb173 { return &sb173{} })
	c.RegisterTransientService[*sb174](func(*sb175) *sb174 { return &sb174{} })
	c.RegisterTransientService[*sb175](func(*sb176) *sb175 { return &sb175{} })
	c.RegisterTransientService[*sb176](func(*sb177) *sb176 { return &sb176{} })
	c.RegisterTransientService[*sb177](func(*sb178) *sb177 { return &sb177{} })
	c.RegisterTransientService[*sb178](func(*sb179) *sb178 { return &sb178{} })
	c.RegisterTransientService[*sb179](func(*sb180) *sb179 { return &sb179{} })
	c.RegisterTransientService[*sb180](func(*sb181) *sb180 { return &sb180{} })
	c.RegisterTransientService[*sb181](func(*sb182) *sb181 { return &sb181{} })
	c.RegisterTransientService[*sb182](func(*sb183) *sb182 { return &sb182{} })
	c.RegisterTransientService[*sb183](func(*sb184) *sb183 { return &sb183{} })
	c.RegisterTransientService[*sb184](func(*sb185) *sb184 { return &sb184{} })
	c.RegisterTransientService[*sb185](func(*sb186) *sb185 { return &sb185{} })
	c.RegisterTransientService[*sb186](func(*sb187) *sb186 { return &sb186{} })
	c.RegisterTransientService[*sb187](func(*sb188) *sb187 { return &sb187{} })
	c.RegisterTransientService[*sb188](func(*sb189) *sb188 { return &sb188{} })
	c.RegisterTransientService[*sb189](func(*sb190) *sb189 { return &sb189{} })
	c.RegisterTransientService[*sb190](func(*sb191) *sb190 { return &sb190{} })
	c.RegisterTransientService[*sb191](func(*sb192) *sb191 { return &sb191{} })
	c.RegisterTransientService[*sb192](func(*sb193) *sb192 { return &sb192{} })
	c.RegisterTransientService[*sb193](func(*sb194) *sb193 { return &sb193{} })
	c.RegisterTransientService[*sb194](func(*sb195) *sb194 { return &sb194{} })
	c.RegisterTransientService[*sb195](func(*sb196) *sb195 { return &sb195{} })
	c.RegisterTransientService[*sb196](func(*sb197) *sb196 { return &sb196{} })
	c.RegisterTransientService[*sb197](func(*sb198) *sb197 { return &sb197{} })
	c.RegisterTransientService[*sb198](func(*sb199) *sb198 { return &sb198{} })
	c.RegisterTransientService[*sb199](func(*sb200) *sb199 { return &sb199{} })
	c.RegisterTransientService[*sb200](func(*sb201) *sb200 { return &sb200{} })
	c.RegisterTransientService[*sb201](func(*sb202) *sb201 { return &sb201{} })
	c.RegisterTransientService[*sb202](func(*sb203) *sb202 { return &sb202{} })
	c.RegisterTransientService[*sb203](func(*sb204) *sb203 { return &sb203{} })
	c.RegisterTransientService[*sb204](func(*sb205) *sb204 { return &sb204{} })
	c.RegisterTransientService[*sb205](func(*sb206) *sb205 { return &sb205{} })
	c.RegisterTransientService[*sb206](func(*sb207) *sb206 { return &sb206{} })
	c.RegisterTransientService[*sb207](func(*sb208) *sb207 { return &sb207{} })
	c.RegisterTransientService[*sb208](func(*sb209) *sb208 { return &sb208{} })
	c.RegisterTransientService[*sb209](func(*sb210) *sb209 { return &sb209{} })
	c.RegisterTransientService[*sb210](func(*sb211) *sb210 { return &sb210{} })
	c.RegisterTransientService[*sb211](func(*sb212) *sb211 { return &sb211{} })
	c.RegisterTransientService[*sb212](func(*sb213) *sb212 { return &sb212{} })
	c.RegisterTransientService[*sb213](func(*sb214) *sb213 { return &sb213{} })
	c.RegisterTransientService[*sb214](func(*sb215) *sb214 { return &sb214{} })
	c.RegisterTransientService[*sb215](func(*sb216) *sb215 { return &sb215{} })
	c.RegisterTransientService[*sb216](func(*sb217) *sb216 { return &sb216{} })
	c.RegisterTransientService[*sb217](func(*sb218) *sb217 { return &sb217{} })
	c.RegisterTransientService[*sb218](func(*sb219) *sb218 { return &sb218{} })
	c.RegisterTransientService[*sb219](func(*sb220) *sb219 { return &sb219{} })
	c.RegisterTransientService[*sb220](func(*sb221) *sb220 { return &sb220{} })
	c.RegisterTransientService[*sb221](func(*sb222) *sb221 { return &sb221{} })
	c.RegisterTransientService[*sb222](func(*sb223) *sb222 { return &sb222{} })
	c.RegisterTransientService[*sb223](func(*sb224) *sb223 { return &sb223{} })
	c.RegisterTransientService[*sb224](func(*sb225) *sb224 { return &sb224{} })
	c.RegisterTransientService[*sb225](func(*sb226) *sb225 { return &sb225{} })
	c.RegisterTransientService[*sb226](func(*sb227) *sb226 { return &sb226{} })
	c.RegisterTransientService[*sb227](func(*sb228) *sb227 { return &sb227{} })
	c.RegisterTransientService[*sb228](func(*sb229) *sb228 { return &sb228{} })
	c.RegisterTransientService[*sb229](func(*sb230) *sb229 { return &sb229{} })
	c.RegisterTransientService[*sb230](func(*sb231) *sb230 { return &sb230{} })
	c.RegisterTransientService[*sb231](func(*sb232) *sb231 { return &sb231{} })
	c.RegisterTransientService[*sb232](func(*sb233) *sb232 { return &sb232{} })
	c.RegisterTransientService[*sb233](func(*sb234) *sb233 { return &sb233{} })
	c.RegisterTransientService[*sb234](func(*sb235) *sb234 { return &sb234{} })
	c.RegisterTransientService[*sb235](func(*sb236) *sb235 { return &sb235{} })
	c.RegisterTransientService[*sb236](func(*sb237) *sb236 { return &sb236{} })
	c.RegisterTransientService[*sb237](func(*sb238) *sb237 { return &sb237{} })
	c.RegisterTransientService[*sb238](func(*sb239) *sb238 { return &sb238{} })
	c.RegisterTransientService[*sb239](func(*sb240) *sb239 { return &sb239{} })
	c.RegisterTransientService[*sb240](func(*sb241) *sb240 { return &sb240{} })
	c.RegisterTransientService[*sb241](func(*sb242) *sb241 { return &sb241{} })
	c.RegisterTransientService[*sb242](func(*sb243) *sb242 { return &sb242{} })
	c.RegisterTransientService[*sb243](func(*sb244) *sb243 { return &sb243{} })
	c.RegisterTransientService[*sb244](func(*sb245) *sb244 { return &sb244{} })
	c.RegisterTransientService[*sb245](func(*sb246) *sb245 { return &sb245{} })
	c.RegisterTransientService[*sb246](func(*sb247) *sb246 { return &sb246{} })
	c.RegisterTransientService[*sb247](func(*sb248) *sb247 { return &sb247{} })
	c.RegisterTransientService[*sb248](func(*sb249) *sb248 { return &sb248{} })
	c.RegisterTransientService[*sb249](func(*sb250) *sb249 { return &sb249{} })
	c.RegisterTransientService[*sb250](func(*sb251) *sb250 { return &sb250{} })
	c.RegisterTransientService[*sb251](func(*sb252) *sb251 { return &sb251{} })
	c.RegisterTransientService[*sb252](func(*sb253) *sb252 { return &sb252{} })
	c.RegisterTransientService[*sb253](func(*sb254) *sb253 { return &sb253{} })
	c.RegisterTransientService[*sb254](func(*sb255) *sb254 { return &sb254{} })
	c.RegisterTransientService[*sb255](func(*sb256) *sb255 { return &sb255{} })
	c.RegisterTransientService[*sb256](func(*sb257) *sb256 { return &sb256{} })
	c.RegisterTransientService[*sb257](func(*sb258) *sb257 { return &sb257{} })
	c.RegisterTransientService[*sb258](func() *sb258 { return &sb258{} })
}

// Docs: core/services.mdx#resolution-errors
// "or constructors nest more than 256 deep"
// Assumption: depth counts the constructors on the chain. 256 must resolve and
// 258 must fail under either way of counting the first; 257 is not asserted.
func TestConstructorNestingLimit(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	registerSA(c)
	registerSB(c)
	if _, err := c.Resolve[*sa001](); err != nil {
		t.Fatalf("a chain 256 constructors deep failed: %v", err)
	}
	if _, err := c.Resolve[*sb001](); !errors.Is(err, gas.ErrCircularDependency) {
		t.Fatalf("a chain 258 constructors deep = %v, want ErrCircularDependency", err)
	}
}
