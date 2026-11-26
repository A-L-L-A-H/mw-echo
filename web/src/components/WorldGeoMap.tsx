import { useMemo, useState } from 'react';
import { Globe2, MapPin, X } from 'lucide-react';
import { MonoBadge } from './UiPrimitives';

export interface WorldGeoEvent {
  id: string;
  title: string;
  primaryDomain: string;
  payloadJson?: string;
  lat?: number;
  lon?: number;
}

interface PlottedGeoEvent {
  event: WorldGeoEvent;
  lat: number;
  lon: number;
  x: number;
  y: number;
}

const SVG_WIDTH = 960;
const SVG_HEIGHT = 480;

const DOMAIN_COLORS: Record<string, string> = {
  geopolitics: '#ff2e88',
  cyber: '#00f0ff',
  economy: '#ffe031',
  climate: '#a3ff12',
  conflict: '#ff334b',
  tech: '#00f0ff',
};

function parseCoordinates(event: WorldGeoEvent): { lat: number; lon: number } | null {
  if (
    typeof event.lat === 'number' &&
    Number.isFinite(event.lat) &&
    typeof event.lon === 'number' &&
    Number.isFinite(event.lon)
  ) {
    if (event.lat >= -90 && event.lat <= 90 && event.lon >= -180 && event.lon <= 180) {
      return { lat: event.lat, lon: event.lon };
    }
  }
  if (!event.payloadJson) {
    return null;
  }
  try {
    const parsed = JSON.parse(event.payloadJson) as Record<string, unknown>;
    const rawLat = parsed.lat ?? parsed.latitude ?? (parsed.geo as Record<string, unknown> | undefined)?.lat;
    const rawLon =
      parsed.lon ??
      parsed.lng ??
      parsed.longitude ??
      (parsed.geo as Record<string, unknown> | undefined)?.lon ??
      (parsed.geo as Record<string, unknown> | undefined)?.lng;
    const latNum = typeof rawLat === 'number' ? rawLat : Number(rawLat);
    const lonNum = typeof rawLon === 'number' ? rawLon : Number(rawLon);
    if (
      Number.isFinite(latNum) &&
      Number.isFinite(lonNum) &&
      latNum >= -90 &&
      latNum <= 90 &&
      lonNum >= -180 &&
      lonNum <= 180
    ) {
      return { lat: latNum, lon: lonNum };
    }
  } catch {
    return null;
  }
  return null;
}

function projectEquirectangular(lat: number, lon: number): { x: number; y: number } {
  const x = ((lon + 180) / 360) * SVG_WIDTH;
  const y = ((90 - lat) / 180) * SVG_HEIGHT;
  return { x, y };
}

function resolveDomainColor(domain: string): string {
  const normalized = domain.trim().toLowerCase();
  return DOMAIN_COLORS[normalized] ?? '#ffe031';
}

export function WorldGeoMap({ events }: { events: WorldGeoEvent[] }) {
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const plotted = useMemo<PlottedGeoEvent[]>(() => {
    const markers: PlottedGeoEvent[] = [];
    for (const ev of events) {
      const coords = parseCoordinates(ev);
      if (!coords) {
        continue;
      }
      const projected = projectEquirectangular(coords.lat, coords.lon);
      markers.push({
        event: ev,
        lat: coords.lat,
        lon: coords.lon,
        x: projected.x,
        y: projected.y,
      });
    }
    return markers;
  }, [events]);

  const selectedMarker = useMemo(
    () => plotted.find((marker) => marker.event.id === selectedId) ?? null,
    [plotted, selectedId],
  );

  const latLines = [-60, -30, 0, 30, 60];
  const lonLines = [-150, -120, -90, -60, -30, 0, 30, 60, 90, 120, 150];

  return (
    <div className="relative border-[2.5px] border-black bg-[#08080c] shadow-[5px_5px_0_#000]">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b-2 border-black bg-[#1b1b2b] px-3.5 py-2">
        <div className="flex items-center gap-2">
          <Globe2 className="h-4 w-4 text-[#00f0ff]" />
          <span className="font-display text-xs tracking-wider text-[#fff6de] uppercase">
            EQUIRECTANGULAR WORLD GEO RADAR
          </span>
        </div>
        <div className="flex items-center gap-2">
          <MonoBadge tone="cyan">{plotted.length} GEO-TAGGED</MonoBadge>
          <MonoBadge tone="neutral">{events.length} TOTAL</MonoBadge>
        </div>
      </div>

      <div className="relative overflow-hidden bg-[#0a0a14]">
        <svg
          viewBox={`0 0 ${SVG_WIDTH} ${SVG_HEIGHT}`}
          className="block h-auto w-full select-none"
          role="img"
          aria-label="Equirectangular World Geo Radar Map"
        >
          {/* Background */}
          <rect x={0} y={0} width={SVG_WIDTH} height={SVG_HEIGHT} fill="#0a0a14" />

          {/* Equirectangular graticule gridlines */}
          {latLines.map((lat) => {
            const y = projectEquirectangular(lat, 0).y;
            return (
              <g key={`lat-${lat}`}>
                <line
                  x1={0}
                  y1={y}
                  x2={SVG_WIDTH}
                  y2={y}
                  stroke={lat === 0 ? '#00f0ff' : '#252540'}
                  strokeOpacity={lat === 0 ? 0.35 : 0.7}
                  strokeWidth={lat === 0 ? 1.25 : 0.75}
                  strokeDasharray={lat === 0 ? '6 4' : '2 4'}
                />
                <text
                  x={6}
                  y={y - 4}
                  fill="#6e6a86"
                  fontSize="9"
                  fontFamily="monospace"
                >
                  {lat === 0 ? 'EQ' : `${lat > 0 ? '+' : ''}${lat}°`}
                </text>
              </g>
            );
          })}

          {lonLines.map((lon) => {
            const x = projectEquirectangular(0, lon).x;
            return (
              <g key={`lon-${lon}`}>
                <line
                  x1={x}
                  y1={0}
                  x2={x}
                  y2={SVG_HEIGHT}
                  stroke={lon === 0 ? '#ff2e88' : '#252540'}
                  strokeOpacity={lon === 0 ? 0.3 : 0.6}
                  strokeWidth={lon === 0 ? 1.25 : 0.75}
                  strokeDasharray="2 4"
                />
                <text
                  x={x + 3}
                  y={SVG_HEIGHT - 6}
                  fill="#6e6a86"
                  fontSize="9"
                  fontFamily="monospace"
                >
                  {lon === 0 ? '0°' : `${lon > 0 ? '+' : ''}${lon}°`}
                </text>
              </g>
            );
          })}

          {/* Simplified equirectangular continent outlines */}
          <g fill="#161628" stroke="#3a3a58" strokeWidth="1.2">
            {/* North America */}
            <polygon points="75,65 185,55 245,95 225,155 170,195 140,175 110,130" />
            {/* South America */}
            <polygon points="225,235 280,245 315,295 285,385 250,415 235,345 215,280" />
            {/* Europe */}
            <polygon points="445,70 535,60 565,105 530,135 460,130" />
            {/* Africa */}
            <polygon points="445,155 535,150 580,205 565,305 515,360 475,305 440,225" />
            {/* Asia */}
            <polygon points="555,60 770,55 835,115 790,195 705,215 620,190 565,140" />
            {/* Australia */}
            <polygon points="755,290 845,285 865,345 815,375 750,345" />
          </g>

          {/* Neon event markers */}
          {plotted.map((marker) => {
            const color = resolveDomainColor(marker.event.primaryDomain);
            const isSelected = marker.event.id === selectedId;
            return (
              <g
                key={marker.event.id}
                transform={`translate(${marker.x}, ${marker.y})`}
                onClick={() => setSelectedId(marker.event.id)}
                className="cursor-pointer"
              >
                <circle
                  r={isSelected ? 13 : 9}
                  fill={color}
                  fillOpacity={isSelected ? 0.3 : 0.18}
                />
                <circle
                  r={isSelected ? 6 : 4.5}
                  fill={color}
                  stroke="#08080c"
                  strokeWidth={1.8}
                />
                <title>
                  {`${marker.event.title} (${marker.lat.toFixed(2)}, ${marker.lon.toFixed(2)})`}
                </title>
              </g>
            );
          })}
        </svg>

        {/* Clickable marker readout */}
        {selectedMarker ? (
          <div className="absolute right-3 bottom-3 left-3 flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#13131d]/95 px-3.5 py-2.5 shadow-[4px_4px_0_#000] sm:left-auto sm:max-w-md">
            <div className="min-w-0 flex-1">
              <div className="mb-1 flex flex-wrap items-center gap-1.5">
                <MapPin className="h-3.5 w-3.5 text-[#00f0ff]" />
                <MonoBadge tone="cyan">{selectedMarker.event.primaryDomain}</MonoBadge>
                <span className="font-mono text-[10px] text-[#a6a2b8]">
                  {selectedMarker.lat.toFixed(3)}°, {selectedMarker.lon.toFixed(3)}°
                </span>
              </div>
              <div className="truncate font-mono text-xs font-extrabold text-[#fff6de]">
                {selectedMarker.event.title}
              </div>
            </div>
            <button
              type="button"
              onClick={() => setSelectedId(null)}
              className="riot-btn bg-[#ff334b] p-1 text-white"
              aria-label="Close marker readout"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );
}
