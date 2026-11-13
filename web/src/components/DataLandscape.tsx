/** Decorative data architecture; it does not represent live system metrics. */
export function DataLandscape() {
  return (
    <svg className="data-landscape" viewBox="0 0 900 500" fill="none" aria-hidden="true">
      <defs>
        <linearGradient id="land-face" x1="0" y1="0" x2="1" y2="1"><stop stopColor="#236b67"/><stop offset="1" stopColor="#0b353c"/></linearGradient>
        <linearGradient id="land-top" x1="0" y1="0" x2="1" y2="1"><stop stopColor="#b7e9d4" stopOpacity=".9"/><stop offset="1" stopColor="#438f88" stopOpacity=".5"/></linearGradient>
        <linearGradient id="land-light" x1="0" y1="1" x2="0" y2="0"><stop stopColor="#a3efd6" stopOpacity=".2"/><stop offset="1" stopColor="#a3efd6" stopOpacity="0"/></linearGradient>
        <radialGradient id="land-halo"><stop stopColor="#6ae1bf" stopOpacity=".2"/><stop offset="1" stopColor="#6ae1bf" stopOpacity="0"/></radialGradient>
      </defs>
      <ellipse cx="460" cy="320" rx="420" ry="170" fill="url(#land-halo)"/>
      <g stroke="#8ad7c1" strokeOpacity=".12">
        {Array.from({ length: 15 }, (_, i) => <path key={`a${i}`} d={`M${-160 + i * 70} 170l600 300`} />)}
        {Array.from({ length: 15 }, (_, i) => <path key={`b${i}`} d={`M${180 + i * 70} 140l-600 300`} />)}
      </g>
      <path d="M30 365C170 445 320 456 510 406S792 285 880 338" stroke="#a8dfc4" strokeOpacity=".45"/>
      <path d="M35 380C190 460 360 470 535 421S800 304 890 354" stroke="#a8dfc4" strokeOpacity=".14"/>
      <path d="m70 305 365-183 376 188-365 183L70 305Z" stroke="#75c3b1" strokeOpacity=".28"/>
      <path d="m135 305 300-150 310 155-300 150-310-155Z" fill="#0a3039" stroke="#6faeaa" strokeOpacity=".4"/>
      <path d="m135 305 310 155v16L135 321v-16Z" fill="#0b3038"/>
      <path d="m445 460 300-150v16L445 476v-16Z" fill="#092a33"/>
      <g stroke="#8be4c7" strokeOpacity=".65">
        <path d="m232 310 100 50 118-59 108 54 104-52" strokeWidth="2"/>
        <path d="m240 325 92 46 118-59 108 54 112-56" strokeDasharray="3 7"/>
        <path d="m450 301 0-85"/>
      </g>
      <g>
        <path d="m348 220 102-51 102 51v103l-102 51-102-51V220Z" fill="url(#land-face)" stroke="#83c9b5" strokeOpacity=".6"/>
        <path d="m348 220 102 51 102-51-102-51-102 51Z" fill="url(#land-top)" stroke="#c7f5dd" strokeOpacity=".7"/>
        <path d="M450 271v103" stroke="#b4ecd3" strokeOpacity=".5"/>
        {[0,1,2,3].map(i => <path key={i} d={`m348 ${242+i*23} 102 51 102-51`} stroke="#8bdbbf" strokeOpacity=".3"/>)}
        <path d="m348 220 102 51 102-51V70L450 19 348 70v150Z" fill="url(#land-light)"/>
        <path d="m397 220 53-26 53 26-53 27-53-27Z" fill="#c3f3d8" fillOpacity=".8"/>
        <path d="m415 219 35-17 35 17-35 18-35-18Z" fill="#387b70"/>
      </g>
      <g fill="url(#land-face)" stroke="#6dafa2" strokeOpacity=".7">
        <path d="m193 276 54-27 54 27v62l-54 27-54-27v-62Z"/>
        <path d="m193 276 54 27 54-27-54-27-54 27Z" fill="url(#land-top)"/>
        <path d="M247 303v62m-54-65 54 27 54-27m-108 21 54 27 54-27"/>
        <path d="m595 273 54-27 54 27v62l-54 27-54-27v-62Z"/>
        <path d="m595 273 54 27 54-27-54-27-54 27Z" fill="url(#land-top)"/>
        <path d="M649 300v62m-54-65 54 27 54-27m-108 21 54 27 54-27"/>
      </g>
      <g fill="#c5edd8">
        <circle cx="332" cy="360" r="4"/><circle cx="558" cy="355" r="4"/><circle cx="780" cy="343" r="3"/><circle cx="129" cy="405" r="3"/>
      </g>
      <g stroke="#bcecd3" strokeOpacity=".5">
        <path d="M247 244v-45h-70M649 240v-42h65M450 150V99h75"/>
      </g>
      <g fill="#9bc5b9" fontSize="12" letterSpacing="2" fontFamily="system-ui, sans-serif">
        <text x="115" y="192">CONNECT</text><text x="721" y="202">SHARE</text><text x="534" y="104">GOVERN</text>
      </g>
    </svg>
  );
}
