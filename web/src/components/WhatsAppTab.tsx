import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Bot,
  Check,
  Cloud,
  Copy,
  Download,
  ExternalLink,
  KeyRound,
  LogOut,
  MessageSquare,
  Phone,
  Plus,
  QrCode,
  RefreshCw,
  Save,
  Search,
  Send,
  Settings2,
  Smartphone,
  Terminal,
  Trash2,
  Wifi,
  X,
} from 'lucide-react';
import {
  createWhatsAppDevice,
  DEFAULT_AGENT_IDS,
  deleteWhatsAppDevice,
  fetchWhatsAppChats,
  fetchWhatsAppMessages,
  loginWhatsAppDevice,
  logoutWhatsAppDevice,
  pairWhatsAppDevice,
  patchWhatsAppDeviceConfig,
  reconnectWhatsAppDevice,
  sendWhatsAppMessage,
  type WhatsAppChat,
  type WhatsAppDevice,
  type WhatsAppDeviceConfigPatch,
  type WhatsAppMessage,
  type WhatsAppStats,
} from '../api';
import { copyTextToClipboard, formatCompactNumber, formatShortTime } from '../utils';
import {
  ComicSticker,
  CopyableSnippet,
  MonoBadge,
  SectionCard,
  StageHeroBanner,
  StatTile,
  type BadgeTone,
} from './UiPrimitives';

interface WhatsAppTabProps {
  stats: WhatsAppStats | null;
  devices: WhatsAppDevice[];
  isLoading: boolean;
  onReloadWhatsApp: () => Promise<void>;
  onNotify: (tone: 'ok' | 'err' | 'info', message: string) => void;
}

interface DeviceConfigDraft {
  label: string;
  webhookUrl: string;
  webhookSecret: string;
  aiAutoReply: boolean;
  aiAgentId: string;
  archiveToPool: boolean;
}

const MW_XV_ENV_BLOCK = `WHATSAPP_DRIVER=gowa
WHATSAPP_GATEWAY_URL=http://mw-echo:4310`;

function resolveDeviceStatusTone(device: WhatsAppDevice): BadgeTone {
  const lowered = (device.status || '').toLowerCase();
  if (device.isConnected || lowered === 'connected' || lowered === 'logged_in') {
    return 'ok';
  }
  if (lowered === 'qr_ready' || lowered === 'pairing' || lowered === 'connecting') {
    return 'yellow';
  }
  if (lowered.includes('err') || lowered === 'failed') {
    return 'err';
  }
  return 'neutral';
}

export function WhatsAppTab({
  stats,
  devices,
  isLoading,
  onReloadWhatsApp,
  onNotify,
}: WhatsAppTabProps) {
  const [addFormOpen, setAddFormOpen] = useState(false);
  const [newDeviceId, setNewDeviceId] = useState('');
  const [newDeviceLabel, setNewDeviceLabel] = useState('');
  const [creatingDevice, setCreatingDevice] = useState(false);

  const [busyDeviceId, setBusyDeviceId] = useState<string | null>(null);
  const [activeQrByDevice, setActiveQrByDevice] = useState<
    Record<string, { qrDataUrl?: string; qrLink?: string; qr?: string }>
  >({});
  const [pairOpenDeviceId, setPairOpenDeviceId] = useState<string | null>(null);
  const [pairPhoneInput, setPairPhoneInput] = useState('');
  const [pairCodeByDevice, setPairCodeByDevice] = useState<Record<string, string>>({});
  const [copiedPairDeviceId, setCopiedPairDeviceId] = useState<string | null>(null);

  const [configOpenDeviceId, setConfigOpenDeviceId] = useState<string | null>(null);
  const [configDraft, setConfigDraft] = useState<DeviceConfigDraft>({
    label: '',
    webhookUrl: '',
    webhookSecret: '',
    aiAutoReply: false,
    aiAgentId: 'default',
    archiveToPool: false,
  });
  const [savingConfig, setSavingConfig] = useState(false);

  const [inboxDeviceId, setInboxDeviceId] = useState('');
  const [chatSearch, setChatSearch] = useState('');
  const [chats, setChats] = useState<WhatsAppChat[]>([]);
  const [loadingChats, setLoadingChats] = useState(false);
  const [selectedChat, setSelectedChat] = useState<WhatsAppChat | null>(null);
  const [messages, setMessages] = useState<WhatsAppMessage[]>([]);
  const [loadingMessages, setLoadingMessages] = useState(false);

  const [composePhone, setComposePhone] = useState('');
  const [composeText, setComposeText] = useState('');
  const [sendingMessage, setSendingMessage] = useState(false);
  const [copiedEnvAll, setCopiedEnvAll] = useState(false);

  const threadEndRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (inboxDeviceId || devices.length === 0) {
      return;
    }
    const connectedDevice = devices.find((dev) => dev.isConnected || dev.isLoggedIn);
    setInboxDeviceId(connectedDevice ? connectedDevice.deviceId : devices[0].deviceId);
  }, [devices, inboxDeviceId]);

  useEffect(() => {
    setActiveQrByDevice((prev) => {
      const nextMap: Record<string, { qrDataUrl?: string; qrLink?: string; qr?: string }> = {};
      let changed = false;
      for (const [devId, qrSnapshot] of Object.entries(prev)) {
        const liveDev = devices.find((dev) => dev.deviceId === devId);
        if (!liveDev || liveDev.isConnected || liveDev.isLoggedIn) {
          changed = true;
          continue;
        }
        const merged = {
          qrDataUrl: liveDev.qrDataUrl || qrSnapshot.qrDataUrl,
          qrLink: liveDev.qrLink || qrSnapshot.qrLink,
          qr: liveDev.qr || qrSnapshot.qr,
        };
        if (
          merged.qrDataUrl !== qrSnapshot.qrDataUrl ||
          merged.qrLink !== qrSnapshot.qrLink ||
          merged.qr !== qrSnapshot.qr
        ) {
          changed = true;
        }
        nextMap[devId] = merged;
      }
      return changed ? nextMap : prev;
    });
  }, [devices]);

  const hasPendingQrScan =
    Object.keys(activeQrByDevice).length > 0 ||
    devices.some((dev) => {
      const st = (dev.status || '').toLowerCase();
      return st === 'qr_ready' || st === 'connecting' || st === 'pairing';
    });

  useEffect(() => {
    if (!hasPendingQrScan) {
      return;
    }
    const pollHandle = window.setInterval(() => {
      void onReloadWhatsApp();
    }, 3000);
    return () => window.clearInterval(pollHandle);
  }, [hasPendingQrScan, onReloadWhatsApp]);

  const loadChats = useCallback(async () => {
    setLoadingChats(true);
    try {
      const reply = await fetchWhatsAppChats({
        deviceId: inboxDeviceId || undefined,
        query: chatSearch || undefined,
        limit: 100,
      });
      setChats(reply.chats ?? []);
    } catch {
      // Backend /api/whatsapp/chats may still be booting.
    } finally {
      setLoadingChats(false);
    }
  }, [inboxDeviceId, chatSearch]);

  useEffect(() => {
    const debounceHandle = window.setTimeout(() => {
      void loadChats();
    }, 180);
    return () => window.clearTimeout(debounceHandle);
  }, [loadChats]);

  const loadMessagesForChat = useCallback(
    async (chat: WhatsAppChat) => {
      setLoadingMessages(true);
      try {
        const reply = await fetchWhatsAppMessages(
          chat.jid,
          chat.deviceId || inboxDeviceId || undefined,
          100,
        );
        setMessages(reply.messages ?? []);
      } catch (caughtError) {
        const message =
          caughtError instanceof Error ? caughtError.message : 'Failed to load messages';
        onNotify('err', message);
      } finally {
        setLoadingMessages(false);
      }
    },
    [inboxDeviceId, onNotify],
  );

  useEffect(() => {
    if (selectedChat) {
      void loadMessagesForChat(selectedChat);
    }
  }, [selectedChat, loadMessagesForChat]);

  useEffect(() => {
    threadEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages.length]);

  const handleCreateDevice = async (event: React.FormEvent) => {
    event.preventDefault();
    const cleanId = newDeviceId.trim().toLowerCase().replace(/[^a-z0-9-_]+/g, '-');
    if (!cleanId) {
      onNotify('err', 'Device ID (e.g. primary-wa) is required.');
      return;
    }
    setCreatingDevice(true);
    try {
      await createWhatsAppDevice({
        deviceId: cleanId,
        label: newDeviceLabel.trim() || cleanId,
      });
      setNewDeviceId('');
      setNewDeviceLabel('');
      setAddFormOpen(false);
      setInboxDeviceId(cleanId);
      onNotify('ok', `Registered handset "${cleanId}".`);
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to add device';
      onNotify('err', message);
    } finally {
      setCreatingDevice(false);
    }
  };

  const handleScanQr = async (device: WhatsAppDevice) => {
    setBusyDeviceId(device.deviceId);
    try {
      const updated = await loginWhatsAppDevice(device.deviceId);
      setActiveQrByDevice((prev) => ({
        ...prev,
        [device.deviceId]: {
          qrDataUrl: updated.qrDataUrl,
          qrLink: updated.qrLink,
          qr: updated.qr,
        },
      }));
      onNotify(
        'info',
        updated.isConnected
          ? `Handset "${device.deviceId}" already connected.`
          : `Hologram QR armed for "${device.deviceId}". Scan in WhatsApp -> Linked Devices.`,
      );
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to start QR login';
      onNotify('err', message);
    } finally {
      setBusyDeviceId(null);
    }
  };

  const handlePairByPhone = async (deviceId: string) => {
    const cleanPhone = pairPhoneInput.replace(/[^0-9]/g, '');
    if (cleanPhone.length < 8) {
      onNotify('err', 'Enter full international phone digits (e.g. 6281234567890).');
      return;
    }
    setBusyDeviceId(deviceId);
    try {
      const reply = await pairWhatsAppDevice(deviceId, cleanPhone);
      const codeValue = reply.pairCode || reply.pairingCode || '';
      if (codeValue) {
        setPairCodeByDevice((prev) => ({ ...prev, [deviceId]: codeValue }));
        onNotify('ok', `8-digit code generated for ${deviceId}: ${codeValue}`);
      } else {
        onNotify('info', `Pairing requested for ${deviceId}. Check status.`);
      }
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Phone pairing failed';
      onNotify('err', message);
    } finally {
      setBusyDeviceId(null);
    }
  };

  const handleReconnectDevice = async (deviceId: string) => {
    setBusyDeviceId(deviceId);
    try {
      await reconnectWhatsAppDevice(deviceId);
      onNotify('ok', `Reconnect fired for "${deviceId}".`);
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Reconnect failed';
      onNotify('err', message);
    } finally {
      setBusyDeviceId(null);
    }
  };

  const handleLogoutDevice = async (deviceId: string) => {
    setBusyDeviceId(deviceId);
    try {
      await logoutWhatsAppDevice(deviceId);
      setActiveQrByDevice((prev) => {
        const nextMap = { ...prev };
        delete nextMap[deviceId];
        return nextMap;
      });
      onNotify('ok', `Logged out "${deviceId}".`);
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Logout failed';
      onNotify('err', message);
    } finally {
      setBusyDeviceId(null);
    }
  };

  const handleDeleteDevice = async (deviceId: string) => {
    setBusyDeviceId(deviceId);
    try {
      await deleteWhatsAppDevice(deviceId);
      if (configOpenDeviceId === deviceId) setConfigOpenDeviceId(null);
      if (inboxDeviceId === deviceId) setInboxDeviceId('');
      onNotify('ok', `Removed handset "${deviceId}".`);
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Delete device failed';
      onNotify('err', message);
    } finally {
      setBusyDeviceId(null);
    }
  };

  const openDeviceConfig = (device: WhatsAppDevice) => {
    if (configOpenDeviceId === device.deviceId) {
      setConfigOpenDeviceId(null);
      return;
    }
    setConfigDraft({
      label: device.label || device.deviceId,
      webhookUrl: device.webhookUrl || '',
      webhookSecret: '',
      aiAutoReply: Boolean(device.aiAutoReply),
      aiAgentId: device.aiAgentId || 'default',
      archiveToPool: Boolean(device.archiveToPool),
    });
    setConfigOpenDeviceId(device.deviceId);
  };

  const handleSaveDeviceConfig = async (deviceId: string) => {
    const patchPayload: WhatsAppDeviceConfigPatch = {
      label: configDraft.label.trim(),
      webhookUrl: configDraft.webhookUrl.trim(),
      aiAutoReply: configDraft.aiAutoReply,
      aiAgentId: configDraft.aiAgentId || 'default',
      archiveToPool: configDraft.archiveToPool,
    };
    if (configDraft.webhookSecret.trim()) {
      patchPayload.webhookSecret = configDraft.webhookSecret.trim();
    }
    setSavingConfig(true);
    try {
      await patchWhatsAppDeviceConfig(deviceId, patchPayload);
      onNotify('ok', `Saved radio config for "${deviceId}".`);
      setConfigOpenDeviceId(null);
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to save config';
      onNotify('err', message);
    } finally {
      setSavingConfig(false);
    }
  };

  const handleSelectChat = (chat: WhatsAppChat) => {
    setSelectedChat(chat);
    setComposePhone(chat.jid);
    if (chat.deviceId) {
      setInboxDeviceId(chat.deviceId);
    }
  };

  const handleSendMessage = async (event: React.FormEvent) => {
    event.preventDefault();
    const activeDevice =
      inboxDeviceId || selectedChat?.deviceId || devices[0]?.deviceId || '';
    const recipient = composePhone.trim() || selectedChat?.jid || '';
    if (!activeDevice) {
      onNotify('err', 'Add and select a WhatsApp device first.');
      return;
    }
    if (!recipient || !composeText.trim()) {
      onNotify('err', 'Recipient phone/JID and message text are required.');
      return;
    }
    setSendingMessage(true);
    try {
      const sent = await sendWhatsAppMessage({
        deviceId: activeDevice,
        phone: recipient,
        message: composeText.trim(),
      });
      setComposeText('');
      onNotify(
        'ok',
        `Transmitted via ${activeDevice}${sent.messageId || sent.id ? ` (${sent.messageId || sent.id})` : ''}.`,
      );
      await loadChats();
      if (selectedChat) {
        await loadMessagesForChat(selectedChat);
      }
      await onReloadWhatsApp();
    } catch (caughtError) {
      const message = caughtError instanceof Error ? caughtError.message : 'Failed to send message';
      onNotify('err', message);
    } finally {
      setSendingMessage(false);
    }
  };

  const handleCopyPairCode = async (deviceId: string, codeValue: string) => {
    const copied = await copyTextToClipboard(codeValue);
    if (copied) {
      setCopiedPairDeviceId(deviceId);
      onNotify('ok', `Copied pairing code ${codeValue}.`);
      window.setTimeout(() => setCopiedPairDeviceId(null), 1800);
    }
  };

  const handleCopyEnvBlock = async () => {
    const copied = await copyTextToClipboard(MW_XV_ENV_BLOCK);
    if (copied) {
      setCopiedEnvAll(true);
      onNotify('ok', 'Copied mw-xv WhatsApp GOWA env block.');
      window.setTimeout(() => setCopiedEnvAll(false), 1800);
    }
  };

  return (
    <div className="space-y-6">
      <StageHeroBanner
        sceneCode="SCENE 04"
        sticker="GOWA COMMS"
        title="TACTICAL WALKIE-TALKIE & CYBER-PAGER"
        subtitle="MULTI-DEVICE WHATSAPP GATEWAY — HOLOGRAM QR SCANNER, 8-DIGIT KEYPAD PAIRING, AI AUTO-REPLY & 9DRIVE ARCHIVE"
        tone="ok"
        actions={
          <>
            {hasPendingQrScan ? <ComicSticker tone="yellow">POLLING 3S</ComicSticker> : null}
            <button
              type="button"
              onClick={() => void onReloadWhatsApp()}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#1b1b2b] px-3 py-2 font-mono text-xs font-extrabold text-[#fff6de]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
              SYNC
            </button>
            <button
              type="button"
              onClick={() => setAddFormOpen((prev) => !prev)}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-2 font-display text-xs text-[#08080c] uppercase"
            >
              <Plus className="h-4 w-4" />
              ADD HANDSET
            </button>
          </>
        }
      />

      {/* Comms Telemetry Stamps */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile
          label="LINKED HANDSETS"
          value={`${stats?.connectedCount ?? devices.filter((dev) => dev.isConnected).length}/${stats?.devicesCount ?? devices.length}`}
          sublabel={hasPendingQrScan ? 'Auto-polling QR / keypad...' : 'Multi-device radio pool'}
          icon={<Smartphone className="h-4 w-4" />}
          tone="ok"
        />
        <StatTile
          label="LIVE THREADS"
          value={formatCompactNumber(stats?.chatsCount ?? chats.length)}
          sublabel="SQLite local comms log"
          icon={<MessageSquare className="h-4 w-4" />}
          tone="cyan"
        />
        <StatTile
          label="MESSAGES LOGGED"
          value={formatCompactNumber(stats?.messagesCount ?? 0)}
          sublabel="Optional 9Drive media vault"
          icon={<Cloud className="h-4 w-4" />}
          tone="yellow"
        />
        <StatTile
          label="COMMS ENGINE"
          value={(stats?.engine || 'gowa').toUpperCase()}
          sublabel="Drop-in mw-xv compatible"
          icon={<Wifi className="h-4 w-4" />}
          tone="magenta"
        />
      </div>

      {/* 1. Devices Panel */}
      <SectionCard
        title="HANDSET BAY // DEVICE SESSIONS"
        subtitle="Pair multiple WhatsApp lines via Hologram QR or 8-Digit Keypad, arm AI Auto-Reply & 9Drive media archive."
        icon={<Smartphone className="h-4 w-4" />}
        sticker="GOWA"
        stickerTone="ok"
        accent="ok"
      >
        {addFormOpen ? (
          <form
            onSubmit={handleCreateDevice}
            className="mb-4 flex flex-wrap items-end gap-2.5 border-[2.5px] border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#ffe031]"
          >
            <div className="min-w-[180px] flex-1">
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                Device ID
              </label>
              <input
                type="text"
                value={newDeviceId}
                onChange={(ev) => setNewDeviceId(ev.target.value)}
                placeholder="e.g. cs-main or ops-wa"
                className="riot-input w-full px-3 py-2 font-mono text-xs"
              />
            </div>
            <div className="min-w-[200px] flex-1">
              <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                Callsign Label
              </label>
              <input
                type="text"
                value={newDeviceLabel}
                onChange={(ev) => setNewDeviceLabel(ev.target.value)}
                placeholder="MoonWitness Ops Radio"
                className="riot-input w-full px-3 py-2 font-mono text-xs"
              />
            </div>
            <button
              type="submit"
              disabled={creatingDevice}
              className="riot-btn inline-flex items-center gap-1.5 bg-[#a3ff12] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
            >
              <Plus className="h-3.5 w-3.5" />
              {creatingDevice ? 'BOOTING...' : 'DEPLOY HANDSET'}
            </button>
            <button
              type="button"
              onClick={() => setAddFormOpen(false)}
              className="riot-btn bg-[#1b1b2b] p-2 text-[#fff6de]"
            >
              <X className="h-4 w-4" />
            </button>
          </form>
        ) : null}

        {devices.length === 0 ? (
          <div className="border-2 border-dashed border-[#3a3a58] bg-[#08080c] py-10 text-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
            NO HANDSETS ONLINE. CLICK &quot;ADD HANDSET&quot; TO LINK VIA QR OR 8-DIGIT KEYPAD.
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            {devices.map((device) => {
              const isBusy = busyDeviceId === device.deviceId;
              const statusTone = resolveDeviceStatusTone(device);
              const qrPayload =
                activeQrByDevice[device.deviceId] ??
                (device.qrDataUrl || device.qrLink
                  ? {
                      qrDataUrl: device.qrDataUrl,
                      qrLink: device.qrLink,
                      qr: device.qr,
                    }
                  : null);
              const pairCode = pairCodeByDevice[device.deviceId] || device.pairingCode || '';
              const isConfigOpen = configOpenDeviceId === device.deviceId;
              const isPairOpen = pairOpenDeviceId === device.deviceId;

              return (
                <article
                  key={device.deviceId}
                  className="space-y-3 border-[2.5px] border-black bg-[#08080c] p-4 shadow-[5px_5px_0_#000]"
                >
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-display text-sm text-[#fff6de] uppercase">
                          {device.label || device.deviceId}
                        </span>
                        <MonoBadge tone="cream">{device.deviceId}</MonoBadge>
                        <MonoBadge tone={statusTone}>{device.status || 'disconnected'}</MonoBadge>
                      </div>
                      <div className="mt-1 font-mono text-[11px] font-bold text-[#00f0ff]">
                        JID: {device.jid || 'UNLINKED'}
                      </div>
                    </div>

                    <div className="flex flex-wrap gap-1">
                      {device.aiAutoReply ? (
                        <MonoBadge tone="magenta">
                          <Bot className="h-3 w-3" />
                          AUTO-AI: {device.aiAgentId || 'default'}
                        </MonoBadge>
                      ) : null}
                      {device.archiveToPool ? (
                        <MonoBadge tone="cyan">
                          <Cloud className="h-3 w-3" />
                          9DRIVE
                        </MonoBadge>
                      ) : null}
                      {device.webhookUrl ? <MonoBadge tone="yellow">WEBHOOK</MonoBadge> : null}
                    </div>
                  </div>

                  {/* Arcade Action Deck */}
                  <div className="flex flex-wrap items-center gap-1.5 border-y-2 border-black bg-[#13131d] p-2">
                    <button
                      type="button"
                      disabled={isBusy}
                      onClick={() => void handleScanQr(device)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#ffe031] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c] disabled:opacity-50"
                    >
                      <QrCode className="h-3.5 w-3.5" />
                      SCAN QR
                    </button>

                    <button
                      type="button"
                      disabled={isBusy}
                      onClick={() =>
                        setPairOpenDeviceId(isPairOpen ? null : device.deviceId)
                      }
                      className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c] disabled:opacity-50"
                    >
                      <Phone className="h-3.5 w-3.5" />
                      PAIR PHONE
                    </button>

                    <button
                      type="button"
                      disabled={isBusy}
                      onClick={() => void handleReconnectDevice(device.deviceId)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#a3ff12] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#08080c] disabled:opacity-50"
                    >
                      <RefreshCw className={`h-3 w-3 ${isBusy ? 'animate-spin' : ''}`} />
                      RECONNECT
                    </button>

                    <button
                      type="button"
                      onClick={() => openDeviceConfig(device)}
                      className={`riot-btn inline-flex items-center gap-1 px-2.5 py-1 font-mono text-[11px] font-extrabold ${
                        isConfigOpen
                          ? 'bg-[#ff2e88] text-white'
                          : 'bg-[#1b1b2b] text-[#fff6de]'
                      }`}
                    >
                      <Settings2 className="h-3 w-3" />
                      CONFIG
                    </button>

                    <button
                      type="button"
                      disabled={isBusy}
                      onClick={() => void handleLogoutDevice(device.deviceId)}
                      className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1 font-mono text-[11px] font-extrabold text-[#ffe031] disabled:opacity-50"
                    >
                      <LogOut className="h-3 w-3" />
                      LOGOUT
                    </button>

                    <button
                      type="button"
                      disabled={isBusy}
                      onClick={() => void handleDeleteDevice(device.deviceId)}
                      className="riot-btn ml-auto inline-flex items-center gap-1 bg-[#ff334b] px-2 py-1 font-mono text-[11px] font-extrabold text-white disabled:opacity-50"
                    >
                      <Trash2 className="h-3 w-3" />
                      DEL
                    </button>
                  </div>

                  {/* Hologram QR Scanner Viewport */}
                  {qrPayload && !device.isConnected && !device.isLoggedIn ? (
                    <div className="relative flex flex-wrap items-center gap-4 border-2 border-[#00f0ff] bg-[#13131d] p-3.5 shadow-[4px_4px_0_#00f0ff]">
                      {qrPayload.qrDataUrl ? (
                        <div className="relative shrink-0 overflow-hidden border-[3px] border-black bg-white p-2.5 shadow-[4px_4px_0_#000]">
                          <img
                            src={qrPayload.qrDataUrl}
                            alt={`WhatsApp QR for ${device.deviceId}`}
                            className="h-44 w-44 object-contain"
                          />
                          <div className="riot-qr-sweep pointer-events-none absolute inset-x-0 top-0 h-1 bg-[#ff2e88]/70 shadow-[0_0_8px_#ff2e88]" />
                        </div>
                      ) : null}
                      <div className="min-w-0 flex-1 space-y-2">
                        <div className="flex items-center justify-between gap-2">
                          <ComicSticker tone="yellow">HOLOGRAM QR LOCK</ComicSticker>
                          <button
                            type="button"
                            onClick={() =>
                              setActiveQrByDevice((prev) => {
                                const nextMap = { ...prev };
                                delete nextMap[device.deviceId];
                                return nextMap;
                              })
                            }
                            className="text-[#a6a2b8] hover:text-[#ffe031]"
                          >
                            <X className="h-4 w-4" />
                          </button>
                        </div>
                        <p className="font-mono text-xs text-[#fff6de]/85">
                          WhatsApp -&gt; Linked Devices -&gt; Link a Device. Auto-polling every 3s.
                        </p>
                        {qrPayload.qrLink ? (
                          <a
                            href={qrPayload.qrLink}
                            target="_blank"
                            rel="noreferrer"
                            className="inline-flex items-center gap-1 font-mono text-[11px] font-extrabold text-[#00f0ff] underline"
                          >
                            <ExternalLink className="h-3 w-3" />
                            OPEN RAW QR LINK
                          </a>
                        ) : null}
                      </div>
                    </div>
                  ) : null}

                  {/* 8-Digit Cyber-Pager Keypad Pairing */}
                  {isPairOpen || pairCode ? (
                    <div className="space-y-2.5 border-2 border-[#00f0ff] bg-[#13131d] p-3 shadow-[4px_4px_0_#00f0ff]">
                      {isPairOpen ? (
                        <div className="flex flex-wrap items-center gap-2">
                          <input
                            type="text"
                            value={pairPhoneInput}
                            onChange={(ev) => setPairPhoneInput(ev.target.value)}
                            placeholder="Phone digits (e.g. 6281234567890)"
                            className="riot-input min-w-[200px] flex-1 px-2.5 py-1.5 font-mono text-xs"
                          />
                          <button
                            type="button"
                            disabled={isBusy}
                            onClick={() => void handlePairByPhone(device.deviceId)}
                            className="riot-btn inline-flex items-center gap-1 bg-[#00f0ff] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c] disabled:opacity-50"
                          >
                            <KeyRound className="h-3.5 w-3.5" />
                            GENERATE 8-DIGIT
                          </button>
                        </div>
                      ) : null}

                      {pairCode ? (
                        <div className="flex flex-wrap items-center justify-between gap-2 border-2 border-black bg-[#08080c] px-3.5 py-2.5 shadow-[4px_4px_0_#ffe031]">
                          <div>
                            <div className="font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                              KEYPAD PAIRING CODE
                            </div>
                            <div className="font-display text-3xl tracking-[0.2em] text-[#ffe031] riot-title-ink">
                              {pairCode}
                            </div>
                          </div>
                          <button
                            type="button"
                            onClick={() => void handleCopyPairCode(device.deviceId, pairCode)}
                            className="riot-btn inline-flex items-center gap-1.5 bg-[#fff6de] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
                          >
                            {copiedPairDeviceId === device.deviceId ? (
                              <Check className="h-3.5 w-3.5" />
                            ) : (
                              <Copy className="h-3.5 w-3.5" />
                            )}
                            {copiedPairDeviceId === device.deviceId ? 'COPIED!' : 'COPY CODE'}
                          </button>
                        </div>
                      ) : null}
                    </div>
                  ) : null}

                  {/* Inline Radio Config Drawer */}
                  {isConfigOpen ? (
                    <div className="space-y-3 border-2 border-[#ff2e88] bg-[#13131d] p-3.5 shadow-[4px_4px_0_#ff2e88]">
                      <div className="flex items-center justify-between border-b-2 border-black pb-2">
                        <ComicSticker tone="magenta">RADIO CONFIG</ComicSticker>
                        <button
                          type="button"
                          onClick={() => setConfigOpenDeviceId(null)}
                          className="text-[#a6a2b8] hover:text-[#ffe031]"
                        >
                          <X className="h-4 w-4" />
                        </button>
                      </div>

                      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
                        <div>
                          <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ffe031]">
                            Label
                          </label>
                          <input
                            type="text"
                            value={configDraft.label}
                            onChange={(ev) =>
                              setConfigDraft((prev) => ({ ...prev, label: ev.target.value }))
                            }
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>
                        <div>
                          <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#ff2e88]">
                            AI Auto-Reply Pilot
                          </label>
                          <select
                            value={configDraft.aiAgentId}
                            onChange={(ev) =>
                              setConfigDraft((prev) => ({ ...prev, aiAgentId: ev.target.value }))
                            }
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs font-bold"
                          >
                            {DEFAULT_AGENT_IDS.map((agentId) => (
                              <option key={agentId} value={agentId}>
                                {agentId}
                              </option>
                            ))}
                          </select>
                        </div>
                      </div>

                      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2">
                        <div>
                          <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#00f0ff]">
                            Webhook URL
                          </label>
                          <input
                            type="text"
                            value={configDraft.webhookUrl}
                            onChange={(ev) =>
                              setConfigDraft((prev) => ({ ...prev, webhookUrl: ev.target.value }))
                            }
                            placeholder="https://api.example.com/webhooks/whatsapp"
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>
                        <div>
                          <label className="mb-1 block font-mono text-[10px] font-extrabold uppercase text-[#a6a2b8]">
                            Webhook HMAC Secret
                          </label>
                          <input
                            type="password"
                            value={configDraft.webhookSecret}
                            onChange={(ev) =>
                              setConfigDraft((prev) => ({
                                ...prev,
                                webhookSecret: ev.target.value,
                              }))
                            }
                            placeholder={
                              device.hasWebhookSecret
                                ? 'Secret saved (leave blank to keep)'
                                : 'whsec_...'
                            }
                            className="riot-input w-full px-2.5 py-1.5 font-mono text-xs"
                          />
                        </div>
                      </div>

                      <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
                        <div className="flex flex-wrap items-center gap-4">
                          <label className="inline-flex cursor-pointer items-center gap-2 font-mono text-xs font-extrabold text-[#fff6de]">
                            <input
                              type="checkbox"
                              checked={configDraft.aiAutoReply}
                              onChange={(ev) =>
                                setConfigDraft((prev) => ({
                                  ...prev,
                                  aiAutoReply: ev.target.checked,
                                }))
                              }
                              className="h-4 w-4 accent-[#ff2e88]"
                            />
                            AI AUTO-REPLY
                          </label>
                          <label className="inline-flex cursor-pointer items-center gap-2 font-mono text-xs font-extrabold text-[#fff6de]">
                            <input
                              type="checkbox"
                              checked={configDraft.archiveToPool}
                              onChange={(ev) =>
                                setConfigDraft((prev) => ({
                                  ...prev,
                                  archiveToPool: ev.target.checked,
                                }))
                              }
                              className="h-4 w-4 accent-[#00f0ff]"
                            />
                            9DRIVE ARCHIVE
                          </label>
                        </div>

                        <button
                          type="button"
                          disabled={savingConfig}
                          onClick={() => void handleSaveDeviceConfig(device.deviceId)}
                          className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-4 py-1.5 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
                        >
                          <Save className="h-3.5 w-3.5" />
                          {savingConfig ? 'SAVING...' : 'LOCK CONFIG'}
                        </button>
                      </div>
                    </div>
                  ) : null}
                </article>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* 2. Live Comms Feed & Composer */}
      <SectionCard
        title="LIVE COMMS FEED & TRANSMITTER"
        subtitle="Inspect synced threads, download 9Drive attachments, and fire direct WhatsApp messages."
        icon={<MessageSquare className="h-4 w-4" />}
        sticker="LIVE"
        stickerTone="cyan"
        accent="cyan"
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={inboxDeviceId}
              onChange={(ev) => {
                setInboxDeviceId(ev.target.value);
                setSelectedChat(null);
                setMessages([]);
              }}
              className="riot-input px-2.5 py-1.5 font-mono text-xs font-bold text-[#ffe031]"
            >
              <option value="">All Handsets</option>
              {devices.map((dev) => (
                <option key={dev.deviceId} value={dev.deviceId}>
                  {dev.label || dev.deviceId} ({dev.status})
                </option>
              ))}
            </select>
            <button
              type="button"
              onClick={() => {
                void loadChats();
                if (selectedChat) {
                  void loadMessagesForChat(selectedChat);
                }
              }}
              className="riot-btn inline-flex items-center gap-1 bg-[#1b1b2b] px-2.5 py-1.5 font-mono text-xs font-extrabold text-[#fff6de]"
            >
              <RefreshCw className={`h-3.5 w-3.5 ${loadingChats ? 'animate-spin' : ''}`} />
              SYNC
            </button>
          </div>
        }
      >
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-12">
          {/* Chat List */}
          <div className="flex flex-col border-2 border-black bg-[#08080c] lg:col-span-4">
            <div className="border-b-2 border-black bg-[#1b1b2b] p-2.5">
              <div className="relative">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-[#ffe031]" />
                <input
                  type="search"
                  value={chatSearch}
                  onChange={(ev) => setChatSearch(ev.target.value)}
                  placeholder="Search frequency / JID..."
                  className="riot-input w-full py-1.5 pr-2.5 pl-8 font-mono text-xs"
                />
              </div>
            </div>

            <div className="max-h-[54vh] flex-1 divide-y divide-[#252538] overflow-y-auto">
              {loadingChats && chats.length === 0 ? (
                <div className="p-4 font-mono text-xs text-[#a6a2b8]">Scanning channels...</div>
              ) : chats.length === 0 ? (
                <div className="p-4 font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                  No threads yet. Transmit below!
                </div>
              ) : (
                chats.map((chat) => {
                  const isActive =
                    selectedChat?.jid === chat.jid && selectedChat?.deviceId === chat.deviceId;
                  return (
                    <button
                      key={`${chat.deviceId}:${chat.jid}`}
                      type="button"
                      onClick={() => handleSelectChat(chat)}
                      className={`block w-full px-3 py-2.5 text-left transition-colors ${
                        isActive
                          ? 'bg-[#ffe031] text-[#08080c]'
                          : 'text-[#fff6de] hover:bg-[#1b1b2b]'
                      }`}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="truncate font-display text-xs uppercase">
                          {chat.name || chat.jid}
                        </span>
                        <div className="flex shrink-0 items-center gap-1">
                          {chat.unreadCount > 0 ? (
                            <MonoBadge tone="magenta">{chat.unreadCount}</MonoBadge>
                          ) : null}
                          <span
                            className={`font-mono text-[10px] font-bold ${
                              isActive ? 'text-[#08080c]/75' : 'text-[#a6a2b8]'
                            }`}
                          >
                            {formatShortTime(chat.lastMessageTime)}
                          </span>
                        </div>
                      </div>
                      <div
                        className={`mt-0.5 truncate font-mono text-[10px] font-bold ${
                          isActive ? 'text-[#08080c]/80' : 'text-[#00f0ff]'
                        }`}
                      >
                        {chat.jid}
                      </div>
                      {chat.lastMessage ? (
                        <div
                          className={`mt-1 line-clamp-1 text-[11px] ${
                            isActive ? 'text-[#08080c]' : 'text-[#a6a2b8]'
                          }`}
                        >
                          {chat.lastMessage}
                        </div>
                      ) : null}
                    </button>
                  );
                })
              )}
            </div>
          </div>

          {/* Message Feed + Transmitter */}
          <div className="flex flex-col border-2 border-black bg-[#08080c] lg:col-span-8">
            <div className="flex items-center justify-between border-b-2 border-black bg-[#1b1b2b] px-3.5 py-2.5">
              <div>
                <div className="font-display text-xs text-[#fff6de] uppercase">
                  {selectedChat ? selectedChat.name || selectedChat.jid : 'DIRECT TRANSMITTER'}
                </div>
                <div className="font-mono text-[10px] font-bold text-[#00f0ff]">
                  {selectedChat
                    ? `${selectedChat.jid} · HANDSET: ${selectedChat.deviceId}`
                    : 'Pick a channel on the left or punch any phone/JID below.'}
                </div>
              </div>
              {selectedChat ? <MonoBadge tone="yellow">{messages.length} MSGS</MonoBadge> : null}
            </div>

            <div className="max-h-[42vh] min-h-[260px] flex-1 space-y-2.5 overflow-y-auto p-3.5">
              {loadingMessages ? (
                <div className="font-mono text-xs text-[#a6a2b8]">Loading signal log...</div>
              ) : messages.length === 0 ? (
                <div className="flex h-full min-h-[200px] items-center justify-center font-mono text-xs font-bold uppercase text-[#a6a2b8]">
                  {selectedChat ? 'NO MESSAGES IN THREAD.' : 'SELECT A CHANNEL TO MONITOR.'}
                </div>
              ) : (
                messages.map((msg) => (
                  <div
                    key={msg.id}
                    className={`flex ${msg.fromMe ? 'justify-end' : 'justify-start'}`}
                  >
                    <div
                      className={`max-w-[82%] border-2 border-black px-3 py-2 text-xs shadow-[3px_3px_0_#000] ${
                        msg.fromMe
                          ? 'bg-[#ffe031] text-[#08080c]'
                          : 'bg-[#13131d] text-[#fff6de]'
                      }`}
                    >
                      <div className="mb-1 flex flex-wrap items-center gap-1.5 font-mono text-[10px] font-extrabold uppercase">
                        <span className={msg.fromMe ? 'text-[#08080c]' : 'text-[#00f0ff]'}>
                          {msg.fromMe ? 'TX // OUT' : msg.senderJid || msg.chatJid}
                        </span>
                        {msg.msgType && msg.msgType !== 'text' ? (
                          <MonoBadge tone="magenta">{msg.msgType}</MonoBadge>
                        ) : null}
                        {msg.status ? <span className="opacity-70">· {msg.status}</span> : null}
                      </div>

                      <div className="whitespace-pre-wrap break-words font-medium leading-relaxed">
                        {msg.body || '(media packet)'}
                      </div>

                      {msg.mediaObjectId ? (
                        <a
                          href={`/api/storage/${encodeURIComponent(msg.mediaObjectId)}/download`}
                          target="_blank"
                          rel="noreferrer"
                          className="mt-1.5 inline-flex items-center gap-1 border-2 border-black bg-[#00f0ff] px-2 py-0.5 font-mono text-[10px] font-extrabold text-[#08080c]"
                        >
                          <Download className="h-3 w-3" />
                          9DRIVE FILE ({msg.mediaObjectId.slice(0, 10)})
                        </a>
                      ) : null}

                      <div className="mt-1 text-right font-mono text-[10px] font-bold opacity-70">
                        {formatShortTime(msg.timestamp)}
                      </div>
                    </div>
                  </div>
                ))
              )}
              <div ref={threadEndRef} />
            </div>

            <form
              onSubmit={handleSendMessage}
              className="space-y-2 border-t-2 border-black bg-[#1b1b2b] p-3"
            >
              <div className="flex flex-wrap items-center gap-2">
                <input
                  type="text"
                  value={composePhone}
                  onChange={(ev) => setComposePhone(ev.target.value)}
                  placeholder="Target phone or JID (e.g. 6281234567890)"
                  className="riot-input min-w-[220px] flex-1 px-2.5 py-1.5 font-mono text-xs"
                />
                <MonoBadge tone="cream">
                  VIA: {inboxDeviceId || selectedChat?.deviceId || devices[0]?.deviceId || 'NONE'}
                </MonoBadge>
              </div>
              <div className="flex gap-2">
                <input
                  type="text"
                  value={composeText}
                  onChange={(ev) => setComposeText(ev.target.value)}
                  placeholder="Type WhatsApp transmission..."
                  className="riot-input min-w-0 flex-1 px-3 py-2 font-mono text-xs"
                />
                <button
                  type="submit"
                  disabled={sendingMessage}
                  className="riot-btn inline-flex shrink-0 items-center gap-1.5 bg-[#a3ff12] px-4 py-2 font-display text-xs text-[#08080c] uppercase disabled:opacity-50"
                >
                  <Send className="h-3.5 w-3.5" />
                  {sendingMessage ? '...' : 'SEND'}
                </button>
              </div>
            </form>
          </div>
        </div>
      </SectionCard>

      {/* 3. Drop-In mw-xv GOWA Cartridge */}
      <SectionCard
        title="MW-XV DROP-IN GOWA CARTRIDGE"
        subtitle="Drop these two env vars into mw-xv to route all WhatsApp traffic straight through mw-echo."
        icon={<Terminal className="h-4 w-4" />}
        sticker="PLUG & PLAY"
        stickerTone="yellow"
        actions={
          <button
            type="button"
            onClick={() => void handleCopyEnvBlock()}
            className="riot-btn inline-flex items-center gap-1.5 bg-[#ffe031] px-3 py-1.5 font-mono text-xs font-extrabold text-[#08080c]"
          >
            {copiedEnvAll ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            {copiedEnvAll ? 'COPIED!' : 'COPY ENV BLOCK'}
          </button>
        }
      >
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          <CopyableSnippet label="MW-XV DRIVER" value="WHATSAPP_DRIVER=gowa" tone="ok" />
          <CopyableSnippet
            label="MW-XV GATEWAY URL"
            value="WHATSAPP_GATEWAY_URL=http://mw-echo:4310"
            tone="cyan"
          />
        </div>
      </SectionCard>
    </div>
  );
}
