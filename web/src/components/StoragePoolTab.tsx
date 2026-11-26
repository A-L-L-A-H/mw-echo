import { useRef, useState } from 'react';
import {
  Cloud,
  Database,
  Download,
  ExternalLink,
  HardDrive,
  RefreshCw,
  Search,
  Server,
  Trash2,
  Upload,
} from 'lucide-react';
import {
  addStorageProvider,
  deleteStorageObject,
  deleteStorageProvider,
  startGDriveOAuth,
  syncStorageProviderQuota,
  updateStorageRoutingPolicy,
  uploadStorageObject,
  type PoolSummary,
  type PooledStorageObject,
  type RoutingPolicy,
  type StorageProviderRecord,
} from '../api';
import { computeUsagePercent, formatBytes, formatShortTime } from '../utils';
import { MonoBadge, SectionCard, StageHeroBanner, StatTile } from './UiPrimitives';

interface StoragePoolTabProps {
  poolSummary: PoolSummary | null;
  objects: PooledStorageObject[];
  isLoading: boolean;
  onReloadStorage: (searchTerm?: string) => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

export function StoragePoolTab({
  poolSummary,
  objects,
  isLoading,
  onReloadStorage,
  onNotify,
}: StoragePoolTabProps) {
  const [policyChoice, setPolicyChoice] = useState<RoutingPolicy>(
    poolSummary?.routingPolicy ?? 'most_free',
  );
  const [savingPolicy, setSavingPolicy] = useState(false);
  const [syncingProviderId, setSyncingProviderId] = useState<string | null>(null);

  const [gdName, setGdName] = useState('');
  const [gdClientId, setGdClientId] = useState('');
  const [gdClientSecret, setGdClientSecret] = useState('');
  const [gdRefreshToken, setGdRefreshToken] = useState('');
  const [gdRootFolder, setGdRootFolder] = useState('mw-echo');
  const [gdPriority, setGdPriority] = useState('10');
  const [connectingGd, setConnectingGd] = useState(false);

  const [s3Name, setS3Name] = useState('');
  const [s3Endpoint, setS3Endpoint] = useState('');
  const [s3Bucket, setS3Bucket] = useState('');
  const [s3Region, setS3Region] = useState('auto');
  const [s3AccessKey, setS3AccessKey] = useState('');
  const [s3SecretKey, setS3SecretKey] = useState('');
  const [s3QuotaGb, setS3QuotaGb] = useState('10');
  const [s3Priority, setS3Priority] = useState('20');
  const [s3ForcePathStyle, setS3ForcePathStyle] = useState(true);
  const [connectingS3, setConnectingS3] = useState(false);

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [uploadProject, setUploadProject] = useState('moonwitness');
  const [uploadLogicalPath, setUploadLogicalPath] = useState('');
  const [uploadProviderId, setUploadProviderId] = useState('');
  const [uploadingFile, setUploadingFile] = useState(false);
  const [objectSearch, setObjectSearch] = useState('');

  const providers = poolSummary?.providers ?? [];
  const totalQuota = poolSummary?.totalQuotaBytes ?? 0;
  const totalUsed = poolSummary?.totalUsedBytes ?? 0;
  const totalFree = poolSummary?.totalFreeBytes ?? 0;
  const combinedPct = computeUsagePercent(totalUsed, totalQuota);

  const handleSavePolicy = async (nextPolicy: RoutingPolicy) => {
    setPolicyChoice(nextPolicy);
    setSavingPolicy(true);
    try {
      await updateStorageRoutingPolicy(nextPolicy);
      onNotify('ok', `Cargo routing policy set to "${nextPolicy}".`);
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message =
        caughtError instanceof Error ? caughtError.message : 'Failed to update routing policy';
      onNotify('err', message);
    } finally {
      setSavingPolicy(false);
    }
  };

  const handleSyncQuota = async (provider: StorageProviderRecord) => {
    setSyncingProviderId(provider.id);
    try {
      const refreshed = await syncStorageProviderQuota(provider.id);
      onNotify(
        'ok',
        `Synced ${refreshed.name}: ${formatBytes(refreshed.freeBytes)} free of ${formatBytes(refreshed.quotaBytes)}.`,
      );
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Quota sync failed';
      onNotify('err', message);
    } finally {
      setSyncingProviderId(null);
    }
  };

  const handleDeleteProvider = async (provider: StorageProviderRecord) => {
    try {
      await deleteStorageProvider(provider.id);
      onNotify('ok', `Undocked tank "${provider.name}".`);
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to remove provider';
      onNotify('err', message);
    }
  };

  const handleAddGDriveToken = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!gdName.trim() || !gdClientId.trim() || !gdClientSecret.trim() || !gdRefreshToken.trim()) {
      onNotify('err', 'Name, Client ID, Client Secret, and Refresh Token are required.');
      return;
    }
    setConnectingGd(true);
    try {
      await addStorageProvider({
        name: gdName.trim(),
        kind: 'gdrive',
        priority: Number(gdPriority) || 10,
        gdrive: {
          clientId: gdClientId.trim(),
          clientSecret: gdClientSecret.trim(),
          refreshToken: gdRefreshToken.trim(),
          rootFolder: gdRootFolder.trim() || 'mw-echo',
        },
      });
      setGdName('');
      setGdRefreshToken('');
      onNotify('ok', 'Google Drive 15 GB tank docked to 9Drive Vault.');
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Google Drive connect failed';
      onNotify('err', message);
    } finally {
      setConnectingGd(false);
    }
  };

  const handleStartGDriveOAuth = async () => {
    if (!gdClientId.trim() || !gdClientSecret.trim()) {
      onNotify('err', 'Enter Google OAuth Client ID and Client Secret first.');
      return;
    }
    setConnectingGd(true);
    try {
      const oauthReply = await startGDriveOAuth({
        name: gdName.trim() || `gdrive-${providers.length + 1}`,
        clientId: gdClientId.trim(),
        clientSecret: gdClientSecret.trim(),
        priority: Number(gdPriority) || 10,
      });
      window.open(oauthReply.authUrl, '_blank', 'noopener,noreferrer');
      onNotify('info', `Opened Google consent tab. Redirect URI: ${oauthReply.redirectUri}`);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'OAuth start failed';
      onNotify('err', message);
    } finally {
      setConnectingGd(false);
    }
  };

  const handleAddS3Bucket = async (event: React.FormEvent) => {
    event.preventDefault();
    if (
      !s3Name.trim() ||
      !s3Endpoint.trim() ||
      !s3Bucket.trim() ||
      !s3AccessKey.trim() ||
      !s3SecretKey.trim()
    ) {
      onNotify('err', 'Name, Endpoint, Bucket, Access Key, and Secret Key are required.');
      return;
    }
    setConnectingS3(true);
    try {
      await addStorageProvider({
        name: s3Name.trim(),
        kind: 's3',
        quotaBytes: (Number(s3QuotaGb) || 10) * 1024 * 1024 * 1024,
        priority: Number(s3Priority) || 20,
        s3: {
          endpoint: s3Endpoint.trim(),
          bucket: s3Bucket.trim(),
          region: s3Region.trim() || 'auto',
          accessKey: s3AccessKey.trim(),
          secretKey: s3SecretKey.trim(),
          forcePathStyle: s3ForcePathStyle,
        },
      });
      setS3Name('');
      setS3SecretKey('');
      onNotify('ok', 'S3 / R2 / Garage tank docked to 9Drive Vault.');
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'S3 bucket connect failed';
      onNotify('err', message);
    } finally {
      setConnectingS3(false);
    }
  };

  const handleUploadObject = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!selectedFile) {
      onNotify('err', 'Choose a cargo file to drop first.');
      return;
    }
    setUploadingFile(true);
    try {
      const storedObj = await uploadStorageObject({
        file: selectedFile,
        project: uploadProject.trim() || 'moonwitness',
        logicalPath: uploadLogicalPath.trim() || undefined,
        providerId: uploadProviderId || undefined,
      });
      setSelectedFile(null);
      setUploadLogicalPath('');
      if (fileInputRef.current) fileInputRef.current.value = '';
      onNotify(
        'ok',
        `Stowed ${storedObj.logicalPath} -> ${storedObj.providerKind}:${storedObj.providerName}.`,
      );
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'File upload failed';
      onNotify('err', message);
    } finally {
      setUploadingFile(false);
    }
  };

  const handleDeleteObject = async (storedObj: PooledStorageObject) => {
    try {
      await deleteStorageObject(storedObj.id);
      onNotify('ok', `Jettisoned ${storedObj.logicalPath}.`);
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Delete object failed';
      onNotify('err', message);
    }
  };

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 08"
        sticker="CARGO VAULT"
        title="9DRIVE POOL // FUEL TANKS & DROPZONE"
        subtitle="STACK FREE 15 GB GOOGLE DRIVE ACCOUNTS + S3/R2/GARAGE BUCKETS — ZERO LOCAL DISK WASTE"
        tone="yellow"
        actions={
          <button
            type="button"
            onClick={() => void onReloadStorage(objectSearch)}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-mono text-xs font-extrabold text-[#08080c]"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
            SYNC TANKS
          </button>
        }
      />

      {/* Fuel Tank Telemetry Stamps */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="DOCKED TANKS"
          value={providers.length}
          sublabel={`Policy: ${poolSummary?.routingPolicy ?? policyChoice}`}
          icon={<Server className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="TOTAL CAPACITY"
          value={formatBytes(totalQuota)}
          sublabel="GDrive 15GB/acct + S3/R2"
          icon={<Cloud className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="CARGO LOADED"
          value={formatBytes(totalUsed)}
          sublabel={`${combinedPct}% tank fill`}
          icon={<Database className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="FREE RESERVES"
          value={formatBytes(totalFree)}
          sublabel="Zero local Windows disk"
          icon={<HardDrive className="h-4 w-4" />}
          tone="ok"
        />
      </div>

      {/* Combined Fuel Gauge + Tank Roster */}
      <SectionCard
        title="9DRIVE FUEL TANK ARRAY"
        subtitle="Uploads stream straight into remote cloud accounts using your active routing policy."
        icon={<Cloud className="h-4 w-4" />}
        sticker="ZERO DISK"
        stickerTone="ok"
        accent="yellow"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-[11px] font-extrabold uppercase text-[#ffe031]">
              ROUTING:
            </span>
            <select
              value={poolSummary?.routingPolicy ?? policyChoice}
              disabled={savingPolicy}
              onChange={(ev) => void handleSavePolicy(ev.target.value as RoutingPolicy)}
              className="riot-input px-2.5 py-1.5 font-mono text-xs font-extrabold text-[#ffe031]"
            >
              <option value="most_free">most_free (Largest Reserve)</option>
              <option value="round_robin">round_robin (Balanced)</option>
              <option value="priority">priority (Strict Order)</option>
            </select>
          </div>
        }
      >
        <div className="mb-4 border-2 border-black bg-[#08080c] p-3.5 shadow-[4px_4px_0_#000]">
          <div className="mb-1.5 flex items-center justify-between font-mono text-xs font-extrabold">
            <span className="text-[#00f0ff]">COMBINED FUEL LEVEL</span>
            <span className="text-[#ffe031]">
              {formatBytes(totalUsed)} / {formatBytes(totalQuota)} ({combinedPct}%)
            </span>
          </div>
          <div className="h-5 w-full overflow-hidden border-2 border-black bg-[#1b1b2b]">
            <div
              className="riot-xp-stripe h-full border-r-2 border-black bg-gradient-to-r from-[#00f0ff] via-[#ffe031] to-[#ff2e88] transition-all"
              style={{ width: `${combinedPct}%` }}
            />
          </div>
        </div>

        <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b-2 border-black bg-[#ffe031] font-mono text-[10px] font-extrabold uppercase text-[#08080c]">
                <th className="px-3 py-2">Tank Account</th>
                <th className="px-3 py-2">Kind</th>
                <th className="px-3 py-2">Target</th>
                <th className="px-3 py-2">Fuel Gauge</th>
                <th className="px-3 py-2">Free</th>
                <th className="px-3 py-2">Pri</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#252538]">
              {providers.length > 0 ? (
                providers.map((provider) => {
                  const pct = computeUsagePercent(provider.usedBytes, provider.quotaBytes);
                  const kindTone =
                    provider.kind === 'gdrive'
                      ? 'yellow'
                      : provider.kind === 's3'
                        ? 'cyan'
                        : 'neutral';
                  return (
                    <tr key={provider.id} className="hover:bg-[#1b1b2b]">
                      <td className="px-3 py-2.5">
                        <div className="font-display text-xs text-[#fff6de] uppercase">
                          {provider.name}
                        </div>
                        <div className="font-mono text-[10px] text-[#a6a2b8]">
                          Synced {formatShortTime(provider.lastSyncAt)}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <MonoBadge tone={kindTone}>{provider.kind}</MonoBadge>
                      </td>
                      <td className="max-w-[200px] truncate px-3 py-2.5 font-mono text-[11px] text-[#a6a2b8]">
                        {provider.accountRef || '—'}
                      </td>
                      <td className="min-w-[170px] px-3 py-2.5">
                        <div className="mb-1 flex justify-between font-mono text-[10px] font-bold text-[#a6a2b8]">
                          <span>{formatBytes(provider.usedBytes)}</span>
                          <span>{formatBytes(provider.quotaBytes)}</span>
                        </div>
                        <div className="h-2.5 w-full overflow-hidden border border-black bg-[#1b1b2b]">
                          <div
                            className={`riot-xp-stripe h-full ${
                              pct > 85 ? 'bg-[#ff334b]' : pct > 65 ? 'bg-[#ffe031]' : 'bg-[#a3ff12]'
                            }`}
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                      </td>
                      <td className="px-3 py-2.5 font-mono font-extrabold text-[#a3ff12]">
                        {formatBytes(provider.freeBytes)}
                      </td>
                      <td className="px-3 py-2.5 font-mono font-bold text-[#fff6de]">
                        {provider.priority}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center justify-end gap-1.5">
                          <button
                            type="button"
                            disabled={syncingProviderId === provider.id}
                            onClick={() => void handleSyncQuota(provider)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2 py-1 font-mono text-[10px] font-extrabold text-[#08080c] disabled:opacity-50"
                          >
                            <RefreshCw
                              className={`h-3 w-3 ${syncingProviderId === provider.id ? 'animate-spin' : ''}`}
                            />
                            SYNC
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleDeleteProvider(provider)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[10px] font-extrabold text-white"
                          >
                            <Trash2 className="h-3 w-3" />
                            DEL
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td
                    colSpan={7}
                    className="px-3 py-6 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]"
                  >
                    NO CLOUD TANKS DOCKED YET. DOCK GOOGLE DRIVE OR S3/R2/GARAGE BELOW!
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>

      {/* Docking Bays: Google Drive & S3/R2/Garage */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <SectionCard
          title="DOCK GOOGLE DRIVE (15 GB FREE)"
          subtitle="1-click OAuth browser consent or paste refresh token."
          icon={<Cloud className="h-4 w-4" />}
          sticker="15 GB"
          stickerTone="yellow"
          accent="yellow"
        >
          <form onSubmit={handleAddGDriveToken} className="space-y-2.5">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="text"
                value={gdName}
                onChange={(ev) => setGdName(ev.target.value)}
                placeholder="Tank label (gdrive-1)"
                className="riot-input px-2.5 py-2 font-mono text-xs sm:col-span-2"
              />
              <input
                type="number"
                value={gdPriority}
                onChange={(ev) => setGdPriority(ev.target.value)}
                placeholder="Priority"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>
            <input
              type="text"
              value={gdClientId}
              onChange={(ev) => setGdClientId(ev.target.value)}
              placeholder="Google OAuth Client ID"
              className="riot-input w-full px-2.5 py-2 font-mono text-xs"
            />
            <input
              type="password"
              value={gdClientSecret}
              onChange={(ev) => setGdClientSecret(ev.target.value)}
              placeholder="Google OAuth Client Secret"
              className="riot-input w-full px-2.5 py-2 font-mono text-xs"
            />
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="password"
                value={gdRefreshToken}
                onChange={(ev) => setGdRefreshToken(ev.target.value)}
                placeholder="Refresh Token (optional for OAuth)"
                className="riot-input px-2.5 py-2 font-mono text-xs sm:col-span-2"
              />
              <input
                type="text"
                value={gdRootFolder}
                onChange={(ev) => setGdRootFolder(ev.target.value)}
                placeholder="Root folder"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>
            <div className="flex flex-wrap gap-2 pt-1">
              <button
                type="button"
                disabled={connectingGd}
                onClick={() => void handleStartGDriveOAuth()}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                OAUTH CONNECT
              </button>
              <button
                type="submit"
                disabled={connectingGd}
                className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-4 py-2 font-mono text-xs font-extrabold text-[#fff6de] uppercase disabled:opacity-50"
              >
                SAVE REFRESH TOKEN
              </button>
            </div>
          </form>
        </SectionCard>

        <SectionCard
          title="DOCK S3 / CLOUDFLARE R2 / GARAGE"
          subtitle="AWS SigV4 compatible object bucket."
          icon={<Database className="h-4 w-4" />}
          sticker="S3 / R2"
          stickerTone="cyan"
          accent="cyan"
        >
          <form onSubmit={handleAddS3Bucket} className="space-y-2.5">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <input
                type="text"
                value={s3Name}
                onChange={(ev) => setS3Name(ev.target.value)}
                placeholder="Bucket label (r2-primary)"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="text"
                value={s3Endpoint}
                onChange={(ev) => setS3Endpoint(ev.target.value)}
                placeholder="https://s3.moonwitness.biz.id"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="text"
                value={s3Bucket}
                onChange={(ev) => setS3Bucket(ev.target.value)}
                placeholder="Bucket name"
                className="riot-input px-2.5 py-2 font-mono text-xs sm:col-span-2"
              />
              <input
                type="text"
                value={s3Region}
                onChange={(ev) => setS3Region(ev.target.value)}
                placeholder="Region"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <input
                type="text"
                value={s3AccessKey}
                onChange={(ev) => setS3AccessKey(ev.target.value)}
                placeholder="Access Key ID"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="password"
                value={s3SecretKey}
                onChange={(ev) => setS3SecretKey(ev.target.value)}
                placeholder="Secret Access Key"
                className="riot-input px-2.5 py-2 font-mono text-xs"
              />
            </div>
            <div className="flex flex-wrap items-center gap-2.5 pt-1">
              <input
                type="number"
                value={s3QuotaGb}
                onChange={(ev) => setS3QuotaGb(ev.target.value)}
                placeholder="Quota GB"
                className="riot-input w-24 px-2.5 py-2 font-mono text-xs"
              />
              <input
                type="number"
                value={s3Priority}
                onChange={(ev) => setS3Priority(ev.target.value)}
                placeholder="Pri"
                className="riot-input w-20 px-2.5 py-2 font-mono text-xs"
              />
              <label className="inline-flex cursor-pointer items-center gap-1.5 font-mono text-[11px] font-bold text-[#fff6de]">
                <input
                  type="checkbox"
                  checked={s3ForcePathStyle}
                  onChange={(ev) => setS3ForcePathStyle(ev.target.checked)}
                  className="h-4 w-4 accent-[#00f0ff]"
                />
                PATH-STYLE
              </label>
              <button
                type="submit"
                disabled={connectingS3}
                className="riot-btn ml-auto inline-flex items-center gap-1.5 bg-[#00f0ff] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
              >
                DOCK BUCKET
              </button>
            </div>
          </form>
        </SectionCard>
      </div>

      {/* Cloud Cargo Dropzone & Manifest */}
      <SectionCard
        title="CLOUD CARGO DROPZONE & MANIFEST"
        subtitle="Upload binary assets directly into pooled cloud tanks and stream downloads on demand."
        icon={<HardDrive className="h-4 w-4" />}
        sticker="DROPZONE"
        stickerTone="magenta"
      >
        <form
          onSubmit={handleUploadObject}
          className="mb-4 grid grid-cols-1 gap-2.5 border-2 border-black bg-[#08080c] p-3.5 shadow-[4px_4px_0_#000] md:grid-cols-12"
        >
          <div className="md:col-span-4">
            <input
              ref={fileInputRef}
              type="file"
              onChange={(ev) => setSelectedFile(ev.target.files?.[0] ?? null)}
              className="riot-input w-full cursor-pointer px-2 py-1.5 font-mono text-xs file:mr-2.5 file:border-2 file:border-black file:bg-[#ffe031] file:px-2 file:py-0.5 file:font-mono file:text-[11px] file:font-extrabold file:text-[#08080c]"
            />
          </div>
          <div className="md:col-span-2">
            <input
              type="text"
              value={uploadProject}
              onChange={(ev) => setUploadProject(ev.target.value)}
              placeholder="project"
              className="riot-input w-full px-2.5 py-2 font-mono text-xs"
            />
          </div>
          <div className="md:col-span-3">
            <input
              type="text"
              value={uploadLogicalPath}
              onChange={(ev) => setUploadLogicalPath(ev.target.value)}
              placeholder="Path (assets/hero.png)"
              className="riot-input w-full px-2.5 py-2 font-mono text-xs"
            />
          </div>
          <div className="md:col-span-2">
            <select
              value={uploadProviderId}
              onChange={(ev) => setUploadProviderId(ev.target.value)}
              className="riot-input w-full px-2.5 py-2 font-mono text-xs font-bold"
            >
              <option value="">Auto ({poolSummary?.routingPolicy ?? policyChoice})</option>
              {providers.map((provider) => (
                <option key={provider.id} value={provider.id}>
                  {provider.name} ({provider.kind})
                </option>
              ))}
            </select>
          </div>
          <div className="md:col-span-1">
            <button
              type="submit"
              disabled={uploadingFile}
              className="riot-btn inline-flex w-full items-center justify-center gap-1 bg-[#a3ff12] px-3 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
            >
              <Upload className="h-3.5 w-3.5" />
              {uploadingFile ? '...' : 'DROP'}
            </button>
          </div>
        </form>

        <div className="mb-3 flex items-center gap-2">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-[#ffe031]" />
            <input
              type="search"
              value={objectSearch}
              onChange={(ev) => setObjectSearch(ev.target.value)}
              onKeyDown={(ev) => {
                if (ev.key === 'Enter') void onReloadStorage(objectSearch);
              }}
              placeholder="Search cargo manifest by path, filename, or MIME..."
              className="riot-input w-full py-2 pr-3 pl-9 font-mono text-xs"
            />
          </div>
          <button
            type="button"
            onClick={() => void onReloadStorage(objectSearch)}
            className="riot-btn bg-[#1b1b2b] px-4 py-2 font-mono text-xs font-extrabold text-[#ffe031]"
          >
            SEARCH
          </button>
        </div>

        <div className="overflow-x-auto border-2 border-black bg-[#08080c]">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b-2 border-black bg-[#ff2e88] font-mono text-[10px] font-extrabold uppercase text-white">
                <th className="px-3 py-2">Logical Path</th>
                <th className="px-3 py-2">Tank</th>
                <th className="px-3 py-2">MIME</th>
                <th className="px-3 py-2">Size</th>
                <th className="px-3 py-2">SHA-256</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#252538]">
              {objects.length > 0 ? (
                objects.map((storedObj) => {
                  const providerTone =
                    storedObj.providerKind === 'gdrive'
                      ? 'yellow'
                      : storedObj.providerKind === 's3'
                        ? 'cyan'
                        : 'neutral';
                  return (
                    <tr key={storedObj.id} className="hover:bg-[#1b1b2b]">
                      <td className="px-3 py-2.5">
                        <span className="font-mono text-[#a6a2b8]">{storedObj.project}/</span>
                        <span className="font-mono font-extrabold text-[#fff6de]">
                          {storedObj.logicalPath}
                        </span>
                      </td>
                      <td className="px-3 py-2.5">
                        <MonoBadge tone={providerTone}>
                          {storedObj.providerKind}:{storedObj.providerName}
                        </MonoBadge>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[11px] text-[#a6a2b8]">
                        {storedObj.mimeType}
                      </td>
                      <td className="px-3 py-2.5 font-mono font-bold text-[#ffe031]">
                        {formatBytes(storedObj.sizeBytes)}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[11px] text-[#a6a2b8]">
                        {storedObj.sha256.slice(0, 12)}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center justify-end gap-1.5">
                          <a
                            href={`/api/storage/${encodeURIComponent(storedObj.id)}/download`}
                            target="_blank"
                            rel="noreferrer"
                            className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2 py-1 font-mono text-[10px] font-extrabold text-[#08080c]"
                          >
                            <Download className="h-3 w-3" />
                            GET
                          </a>
                          <button
                            type="button"
                            onClick={() => void handleDeleteObject(storedObj)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[10px] font-extrabold text-white"
                          >
                            <Trash2 className="h-3 w-3" />
                            DEL
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })
              ) : (
                <tr>
                  <td
                    colSpan={6}
                    className="px-3 py-6 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]"
                  >
                    CARGO MANIFEST EMPTY.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>
    </div>
  );
}
