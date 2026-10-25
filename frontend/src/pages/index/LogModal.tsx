import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button, Checkbox, Form, Modal, Select, Space } from 'antd';
import { DownloadOutlined, SyncOutlined } from '@ant-design/icons';

import { HttpUtil, FileManager, PromiseUtil } from '@/utils';
import { activateOnKey } from '@/utils/a11y';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { parseLogLine } from './logParse';
import './LogModal.css';

// A log other than the panel's own, read from path/<rows> with the level in the form.
export interface LogSource {
  path: string;
  title: string;
  fileName: string;
}

interface LogModalProps {
  open: boolean;
  onClose: () => void;
  source?: LogSource;
}

const AUTO_UPDATE_INTERVAL = 5000;
const NO_LINES: string[] = [];

export default function LogModal({ open, onClose, source }: LogModalProps) {
  const { t } = useTranslation();
  const { isMobile } = useMediaQuery();
  const [rows, setRows] = useState('20');
  const [level, setLevel] = useState('info');
  const [syslog, setSyslog] = useState(false);
  const [autoUpdate, setAutoUpdate] = useState(false);
  const [loading, setLoading] = useState(false);
  // The lines come with the source they were read from, so that a dialog that moves on to another
  // log, or whose refresh fails, never shows one log under the title of another.
  const [fetched, setFetched] = useState<{ path?: string; lines: string[] }>({ lines: NO_LINES });
  const sourcePath = source?.path;
  const logs = fetched.path === sourcePath ? fetched.lines : NO_LINES;
  const currentPath = useRef(sourcePath);
  const busy = useRef(false);
  useEffect(() => {
    currentPath.current = sourcePath;
  });

  const runRefresh = useCallback(async () => {
    busy.current = true;
    try {
      const msg = await HttpUtil.post<string[]>(
        `${sourcePath ?? '/panel/api/server/logs'}/${rows}`,
        sourcePath ? { level } : { level, syslog },
      );
      // An answer for a log the dialog has left is not for the one it shows now.
      if (msg?.success && currentPath.current === sourcePath) {
        setFetched({ path: sourcePath, lines: msg.obj || [] });
      }
      await PromiseUtil.sleep(300);
    } finally {
      busy.current = false;
      setLoading(false);
    }
  }, [rows, level, syslog, sourcePath]);

  const refresh = useCallback(() => {
    setLoading(true);
    void runRefresh();
  }, [runRefresh]);

  const refreshRef = useRef(refresh);
  useEffect(() => {
    refreshRef.current = refresh;
  });

  // The spinner is raised during render so the fetch effect stays side-effect
  // free until its response lands.
  const refreshKey = open ? `${sourcePath}\u0000${rows}\u0000${level}\u0000${syslog}` : null;
  const [loadingKey, setLoadingKey] = useState<string | null>(null);
  if (refreshKey !== loadingKey) {
    setLoadingKey(refreshKey);
    if (refreshKey) setLoading(true);
  }

  useEffect(() => {
    if (open) void runRefresh();
  }, [open, runRefresh]);

  useEffect(() => {
    if (!open || !autoUpdate) return;
    const id = setInterval(() => {
      if (!busy.current) refreshRef.current();
    }, AUTO_UPDATE_INTERVAL);
    return () => clearInterval(id);
  }, [open, autoUpdate]);

  const parsedLogs = useMemo(() => logs.map(parseLogLine), [logs]);

  function download() {
    FileManager.downloadTextFile(logs.join('\n'), source?.fileName ?? 'x-ui.log');
  }

  const titleNode = (
    <>
      {source?.title ?? t('pages.index.logs')}
      <SyncOutlined
        spin={loading}
        className="reload-icon"
        role="button"
        tabIndex={0}
        aria-label={t('refresh')}
        onClick={refresh}
        onKeyDown={(event) => activateOnKey(refresh)(event)}
      />
    </>
  );

  return (
    <Modal
      open={open}
      footer={null}
      width={isMobile ? '100vw' : 800}
      style={isMobile ? { top: 0, paddingBottom: 0, maxWidth: '100vw' } : undefined}
      className={isMobile ? 'logmodal-mobile' : undefined}
      onCancel={onClose}
      title={titleNode}
    >
      <Form layout="inline" className="log-toolbar">
        <Form.Item>
          <Space.Compact>
            <Select
              aria-label={t('pages.index.logLines')}
              value={rows}
              size="small"
              style={{ width: 100 }}
              onChange={setRows}
              options={[
                { value: '20', label: '20' },
                { value: '50', label: '50' },
                { value: '100', label: '100' },
                { value: '500', label: '500' },
                { value: '1000', label: '1000' },
              ]}
            />
            <Select
              aria-label={t('pages.xray.logLevel')}
              value={level}
              size="small"
              style={{ minWidth: 95 }}
              onChange={setLevel}
              options={[
                { value: 'debug', label: t('pages.index.logLevelDebug') },
                { value: 'info', label: t('pages.index.logLevelInfo') },
                { value: 'notice', label: t('pages.index.logLevelNotice') },
                { value: 'warning', label: t('pages.index.logLevelWarning') },
                { value: 'err', label: t('pages.index.logLevelError') },
              ]}
            />
          </Space.Compact>
        </Form.Item>
        <Form.Item>
          {!source && (
            <Checkbox checked={syslog} onChange={(e) => setSyslog(e.target.checked)}>
              SysLog
            </Checkbox>
          )}
          <Checkbox checked={autoUpdate} onChange={(e) => setAutoUpdate(e.target.checked)}>
            {t('pages.index.autoUpdate')}
          </Checkbox>
        </Form.Item>
        <Form.Item className="download-item">
          <Button
            type="primary"
            onClick={download}
            icon={<DownloadOutlined aria-hidden />}
            aria-label={t('download')}
          />
        </Form.Item>
      </Form>

      <div className={`log-container ${isMobile ? 'log-container-mobile' : ''}`}>
        {parsedLogs.length === 0 ? (
          <div className="log-empty">No Record...</div>
        ) : isMobile ? (
          parsedLogs.map((log, idx) => (
            <div key={idx} className="log-card">
              <div className="log-card-head">
                {log.stamp && (
                  <span className="log-time">
                    {log.time && <span>{log.time}</span>}
                    {log.time && log.date ? ' ' : ''}
                    {log.date && <span className="log-date">{log.date}</span>}
                  </span>
                )}
                {log.levelText && (
                  <span className={`log-level-badge ${log.levelClass}`}>{log.levelText}</span>
                )}
              </div>
              {(log.body || log.service) && (
                <div className="log-body">
                  {log.service && <b>{log.service}</b>}
                  {log.service && log.body ? ' ' : ''}
                  {log.body && <span className="log-body-text">{log.body}</span>}
                </div>
              )}
            </div>
          ))
        ) : (
          parsedLogs.map((log, idx) => (
            <div key={idx} className="log-line">
              {log.stamp && <span className="log-stamp">{log.stamp}</span>}
              {log.stamp && log.levelText ? ' ' : ''}
              {log.levelText && (
                <span className={`log-level ${log.levelClass}`}>{log.levelText}</span>
              )}
              {(log.body || log.service) && (
                <>
                  {(log.stamp || log.levelText) && <span> - </span>}
                  {log.service && <b>{log.service}</b>}
                  {log.service && log.body ? ' ' : ''}
                  <span>{log.body}</span>
                </>
              )}
            </div>
          ))
        )}
      </div>
    </Modal>
  );
}
