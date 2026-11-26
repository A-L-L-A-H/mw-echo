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
import { MonoBadge, SectionCard, StatTile } from './UiPrimitives';

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

  // Google Drive connect form
  const [gdName, setGdName] = useState('');
  const [gdClientId, setGdClientId] = useState('');
  const [gdClientSecret, setGdClientSecret] = useState('');
  const [gdRefreshToken, setGdRefreshToken] = useState('');
  const [gdRootFolder, setGdRootFolder] = useState('mw-echo');
  const [gdPriority, setGdPriority] = useState('10');
  const [connectingGd, setConnectingGd] = useState(false);

  // S3 / R2 / Garage connect form
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

  // File upload & browser state
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
      onNotify('ok', `Upload routing policy updated to "${nextPolicy}".`);
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
      onNotify('ok', `Removed storage provider "${provider.name}".`);
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
      onNotify('ok', 'Google Drive account connected to 9Drive pool.');
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
      onNotify(
        'info',
        `Opened Google consent tab. Authorized redirect URI: ${oauthReply.redirectUri}`,
      );
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
      onNotify('ok', 'S3 / R2 / Garage bucket connected to 9Drive pool.');
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
      onNotify('err', 'Choose a file to upload first.');
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
      if (fileInputRef.current) {
        fileInputRef.current.value = '';
      }
      onNotify(
        'ok',
        `Uploaded ${storedObj.logicalPath} -> ${storedObj.providerKind}:${storedObj.providerName}.`,
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
      onNotify('ok', `Deleted ${storedObj.logicalPath}.`);
      await onReloadStorage(objectSearch);
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Delete object failed';
      onNotify('err', message);
    }
  };

  return (
    <div className="space-y-5">
      {/* Pool Quota Metrics */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="Connected Providers"
          value={providers.length}
          sublabel={`Policy: ${poolSummary?.routingPolicy ?? policyChoice}`}
          icon={<Server className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="Combined Pool Quota"
          value={formatBytes(totalQuota)}
          sublabel="Google Drive (15 GB/acct) + S3/R2"
          icon={<Cloud className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="Combined Used"
          value={formatBytes(totalUsed)}
          sublabel={`${combinedPct}% pool utilization`}
          icon={<Database className="h-4 w-4" />}
          tone="magenta"
        />
        <StatTile
          label="Combined Free Space"
          value={formatBytes(totalFree)}
          sublabel="Zero local Windows disk usage"
          icon={<HardDrive className="h-4 w-4" />}
          tone="ok"
        />
      </div>

      {/* Combined Quota Meter + Policy + Connected Providers */}
      <SectionCard
        title="9Drive Multi-Account Storage Pool"
        subtitle="Uploads stream straight to remote cloud accounts according to the active routing policy."
        icon={<Cloud className="h-4 w-4" />}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-[11px] uppercase text-[#9c988c]">Routing Policy:</span>
            <select
              value={poolSummary?.routingPolicy ?? policyChoice}
              disabled={savingPolicy}
              onChange={(ev) => void handleSavePolicy(ev.target.value as RoutingPolicy)}
              className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-1.5 font-mono text-xs text-[#ffe031] focus:border-[#ffe031] focus:outline-none"
            >
              <option value="most_free">most_free (Largest Free Space)</option>
              <option value="round_robin">round_robin (Balanced Spread)</option>
              <option value="priority">priority (Strict Priority Order)</option>
            </select>
            <button
              type="button"
              onClick={() => void onReloadStorage(objectSearch)}
              className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2.5 py-1.5 font-mono text-[11px] text-[#f3efe4] hover:border-[#ffe031]"
            >
              <RefreshCw className={`h-3 w-3 ${isLoading ? 'animate-spin' : ''}`} />
              REFRESH
            </button>
          </div>
        }
      >
        {/* Combined Progress Meter */}
        <div className="mb-4 rounded-md border border-[#2a2a36] bg-[#0d0d10] p-3">
          <div className="mb-1.5 flex items-center justify-between font-mono text-xs">
            <span className="text-[#9c988c]">COMBINED POOL CAPACITY</span>
            <span className="font-bold text-[#f3efe4]">
              {formatBytes(totalUsed)} / {formatBytes(totalQuota)} ({combinedPct}%)
            </span>
          </div>
          <div className="h-2.5 w-full overflow-hidden rounded-full bg-[#1c1c23]">
            <div
              className="h-full rounded-full bg-gradient-to-r from-[#00b8f0] via-[#ffe031] to-[#ff2e88] transition-all"
              style={{ width: `${combinedPct}%` }}
            />
          </div>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Account</th>
                <th className="px-3 py-2">Kind</th>
                <th className="px-3 py-2">Target / Ref</th>
                <th className="px-3 py-2">Quota Meter</th>
                <th className="px-3 py-2">Free</th>
                <th className="px-3 py-2">Priority</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
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
                    <tr key={provider.id} className="hover:bg-[#1c1c23]/60">
                      <td className="px-3 py-2.5">
                        <div className="font-mono font-bold text-[#f3efe4]">{provider.name}</div>
                        <div className="font-mono text-[10px] text-[#9c988c]">
                          Synced: {formatShortTime(provider.lastSyncAt)}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <MonoBadge tone={kindTone}>{provider.kind}</MonoBadge>
                      </td>
                      <td className="max-w-[200px] truncate px-3 py-2.5 font-mono text-[11px] text-[#9c988c]">
                        {provider.accountRef || '—'}
                      </td>
                      <td className="min-w-[170px] px-3 py-2.5">
                        <div className="mb-1 flex justify-between font-mono text-[10px] text-[#9c988c]">
                          <span>{formatBytes(provider.usedBytes)}</span>
                          <span>{formatBytes(provider.quotaBytes)}</span>
                        </div>
                        <div className="h-1.5 w-full overflow-hidden rounded-full bg-[#1c1c23]">
                          <div
                            className={`h-full rounded-full ${
                              pct > 85 ? 'bg-[#ef4444]' : pct > 65 ? 'bg-[#ffe031]' : 'bg-[#22c55e]'
                            }`}
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                      </td>
                      <td className="px-3 py-2.5 font-mono font-bold text-[#22c55e]">
                        {formatBytes(provider.freeBytes)}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[#9c988c]">{provider.priority}</td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center justify-end gap-1.5">
                          <button
                            type="button"
                            disabled={syncingProviderId === provider.id}
                            onClick={() => void handleSyncQuota(provider)}
                            className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2 py-1 font-mono text-[10px] font-semibold text-[#00b8f0] hover:border-[#00b8f0] disabled:opacity-50"
                          >
                            <RefreshCw
                              className={`h-3 w-3 ${syncingProviderId === provider.id ? 'animate-spin' : ''}`}
                            />
                            SYNC
                          </button>
                          <button
                            type="button"
                            onClick={() => void handleDeleteProvider(provider)}
                            className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2 py-1 font-mono text-[10px] font-semibold text-[#ef4444] hover:bg-[#ef4444]/20"
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
                  <td colSpan={7} className="px-3 py-6 text-center font-mono text-xs text-[#9c988c]">
                    No cloud accounts connected yet. Uploads automatically fall back to built-in
                    local disk until you connect Google Drive or S3/R2/Garage below.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>

      {/* Connect Google Drive + Connect S3/R2/Garage */}
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
        <SectionCard
          title="Connect Google Drive (15 GB Free / Account)"
          subtitle="Use 1-click OAuth browser consent or paste an existing OAuth refresh token."
          icon={<Cloud className="h-4 w-4" />}
        >
          <form onSubmit={handleAddGDriveToken} className="space-y-2.5">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="text"
                value={gdName}
                onChange={(ev) => setGdName(ev.target.value)}
                placeholder="Account label (gdrive-1)"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none sm:col-span-2"
              />
              <input
                type="number"
                value={gdPriority}
                onChange={(ev) => setGdPriority(ev.target.value)}
                placeholder="Priority"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
            <input
              type="text"
              value={gdClientId}
              onChange={(ev) => setGdClientId(ev.target.value)}
              placeholder="Google OAuth Client ID"
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
            <input
              type="password"
              value={gdClientSecret}
              onChange={(ev) => setGdClientSecret(ev.target.value)}
              placeholder="Google OAuth Client Secret"
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="password"
                value={gdRefreshToken}
                onChange={(ev) => setGdRefreshToken(ev.target.value)}
                placeholder="Refresh Token (optional for OAuth)"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none sm:col-span-2"
              />
              <input
                type="text"
                value={gdRootFolder}
                onChange={(ev) => setGdRootFolder(ev.target.value)}
                placeholder="Root folder"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
            <div className="flex flex-wrap gap-2 pt-1">
              <button
                type="button"
                disabled={connectingGd}
                onClick={() => void handleStartGDriveOAuth()}
                className="inline-flex items-center gap-1.5 rounded bg-[#ffe031] px-3.5 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
              >
                <ExternalLink className="h-3.5 w-3.5" />
                OAUTH CONNECT
              </button>
              <button
                type="submit"
                disabled={connectingGd}
                className="inline-flex items-center gap-1.5 rounded border border-[#2a2a36] bg-[#1c1c23] px-3.5 py-2 font-mono text-xs font-bold text-[#f3efe4] hover:border-[#ffe031] hover:text-[#ffe031] disabled:opacity-50"
              >
                SAVE REFRESH TOKEN
              </button>
            </div>
          </form>
        </SectionCard>

        <SectionCard
          title="Connect S3 / Cloudflare R2 / Garage"
          subtitle="AWS SigV4 compatible object storage bucket (zero local disk footprint)."
          icon={<Database className="h-4 w-4" />}
        >
          <form onSubmit={handleAddS3Bucket} className="space-y-2.5">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <input
                type="text"
                value={s3Name}
                onChange={(ev) => setS3Name(ev.target.value)}
                placeholder="Bucket label (r2-primary)"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="text"
                value={s3Endpoint}
                onChange={(ev) => setS3Endpoint(ev.target.value)}
                placeholder="https://s3.moonwitness.biz.id"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <input
                type="text"
                value={s3Bucket}
                onChange={(ev) => setS3Bucket(ev.target.value)}
                placeholder="Bucket name"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none sm:col-span-2"
              />
              <input
                type="text"
                value={s3Region}
                onChange={(ev) => setS3Region(ev.target.value)}
                placeholder="Region (auto)"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
              <input
                type="text"
                value={s3AccessKey}
                onChange={(ev) => setS3AccessKey(ev.target.value)}
                placeholder="Access Key ID"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="password"
                value={s3SecretKey}
                onChange={(ev) => setS3SecretKey(ev.target.value)}
                placeholder="Secret Access Key"
                className="rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
            </div>
            <div className="flex flex-wrap items-center gap-2.5 pt-1">
              <input
                type="number"
                value={s3QuotaGb}
                onChange={(ev) => setS3QuotaGb(ev.target.value)}
                placeholder="Quota GB"
                className="w-24 rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <input
                type="number"
                value={s3Priority}
                onChange={(ev) => setS3Priority(ev.target.value)}
                placeholder="Priority"
                className="w-24 rounded border border-[#2a2a36] bg-[#0d0d10] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
              />
              <label className="inline-flex cursor-pointer items-center gap-1.5 font-mono text-[11px] text-[#9c988c]">
                <input
                  type="checkbox"
                  checked={s3ForcePathStyle}
                  onChange={(ev) => setS3ForcePathStyle(ev.target.checked)}
                  className="accent-[#ffe031]"
                />
                Path-Style
              </label>
              <button
                type="submit"
                disabled={connectingS3}
                className="ml-auto inline-flex items-center gap-1.5 rounded bg-[#00b8f0] px-3.5 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
              >
                ADD S3 BUCKET
              </button>
            </div>
          </form>
        </SectionCard>
      </div>

      {/* Zero-Local-Disk File Upload & Object Browser */}
      <SectionCard
        title="Zero-Local-Disk Storage Browser"
        subtitle="Upload binary assets directly into the pooled cloud accounts and stream downloads by object ID."
        icon={<HardDrive className="h-4 w-4" />}
      >
        <form
          onSubmit={handleUploadObject}
          className="mb-4 grid grid-cols-1 gap-2.5 rounded-lg border border-[#2a2a36] bg-[#0d0d10] p-3.5 md:grid-cols-12"
        >
          <div className="md:col-span-4">
            <input
              ref={fileInputRef}
              type="file"
              onChange={(ev) => setSelectedFile(ev.target.files?.[0] ?? null)}
              className="w-full cursor-pointer rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-1.5 font-mono text-xs text-[#f3efe4] file:mr-2.5 file:rounded file:border-0 file:bg-[#ffe031] file:px-2 file:py-0.5 file:font-mono file:text-[11px] file:font-bold file:text-[#0d0d10]"
            />
          </div>
          <div className="md:col-span-2">
            <input
              type="text"
              value={uploadProject}
              onChange={(ev) => setUploadProject(ev.target.value)}
              placeholder="project"
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>
          <div className="md:col-span-3">
            <input
              type="text"
              value={uploadLogicalPath}
              onChange={(ev) => setUploadLogicalPath(ev.target.value)}
              placeholder="Optional path (assets/hero.png)"
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>
          <div className="md:col-span-2">
            <select
              value={uploadProviderId}
              onChange={(ev) => setUploadProviderId(ev.target.value)}
              className="w-full rounded border border-[#2a2a36] bg-[#15151a] px-2.5 py-2 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
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
              className="inline-flex w-full items-center justify-center gap-1 rounded bg-[#ffe031] px-3 py-2 font-mono text-xs font-extrabold text-[#0d0d10] hover:opacity-90 disabled:opacity-50"
            >
              <Upload className="h-3.5 w-3.5" />
              {uploadingFile ? '...' : 'PUT'}
            </button>
          </div>
        </form>

        <div className="mb-3 flex items-center gap-2">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-[#9c988c]" />
            <input
              type="search"
              value={objectSearch}
              onChange={(ev) => setObjectSearch(ev.target.value)}
              onKeyDown={(ev) => {
                if (ev.key === 'Enter') {
                  void onReloadStorage(objectSearch);
                }
              }}
              placeholder="Filter stored objects by path, filename, or MIME..."
              className="w-full rounded border border-[#2a2a36] bg-[#0d0d10] py-2 pr-3 pl-9 font-mono text-xs text-[#f3efe4] focus:border-[#ffe031] focus:outline-none"
            />
          </div>
          <button
            type="button"
            onClick={() => void onReloadStorage(objectSearch)}
            className="rounded border border-[#2a2a36] bg-[#1c1c23] px-3.5 py-2 font-mono text-xs font-semibold text-[#f3efe4] hover:border-[#ffe031]"
          >
            SEARCH
          </button>
        </div>

        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-left text-xs">
            <thead>
              <tr className="border-b border-[#2a2a36] font-mono text-[10px] uppercase tracking-wider text-[#9c988c]">
                <th className="px-3 py-2">Logical Path</th>
                <th className="px-3 py-2">Provider</th>
                <th className="px-3 py-2">MIME</th>
                <th className="px-3 py-2">Size</th>
                <th className="px-3 py-2">SHA-256</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[#2a2a36]/70">
              {objects.length > 0 ? (
                objects.map((storedObj) => {
                  const providerTone =
                    storedObj.providerKind === 'gdrive'
                      ? 'yellow'
                      : storedObj.providerKind === 's3'
                        ? 'cyan'
                        : 'neutral';
                  return (
                    <tr key={storedObj.id} className="hover:bg-[#1c1c23]/60">
                      <td className="px-3 py-2.5">
                        <span className="font-mono text-[#9c988c]">{storedObj.project}/</span>
                        <span className="font-mono font-bold text-[#f3efe4]">
                          {storedObj.logicalPath}
                        </span>
                      </td>
                      <td className="px-3 py-2.5">
                        <MonoBadge tone={providerTone}>
                          {storedObj.providerKind}:{storedObj.providerName}
                        </MonoBadge>
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[11px] text-[#9c988c]">
                        {storedObj.mimeType}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[#f3efe4]">
                        {formatBytes(storedObj.sizeBytes)}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-[11px] text-[#9c988c]">
                        {storedObj.sha256.slice(0, 12)}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center justify-end gap-1.5">
                          <a
                            href={`/api/storage/${encodeURIComponent(storedObj.id)}/download`}
                            target="_blank"
                            rel="noreferrer"
                            className="inline-flex items-center gap-1 rounded border border-[#2a2a36] bg-[#1c1c23] px-2 py-1 font-mono text-[10px] font-semibold text-[#00b8f0] hover:border-[#00b8f0]"
                          >
                            <Download className="h-3 w-3" />
                            GET
                          </a>
                          <button
                            type="button"
                            onClick={() => void handleDeleteObject(storedObj)}
                            className="inline-flex items-center gap-1 rounded border border-[#ef4444]/40 bg-[#ef4444]/10 px-2 py-1 font-mono text-[10px] font-semibold text-[#ef4444] hover:bg-[#ef4444]/20"
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
                  <td colSpan={6} className="px-3 py-6 text-center font-mono text-xs text-[#9c988c]">
                    No stored objects found.
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
