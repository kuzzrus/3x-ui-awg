import type { ReactNode } from 'react';
import { QuestionCircleOutlined } from '@ant-design/icons';
import { ConfigProvider } from 'antd';
import type { ConfigProviderProps } from 'antd/es/config-provider';
import type { Locale } from 'antd/es/locale';

const antdLocaleLoaders: Record<string, () => Promise<{ default: Locale }>> = {
  'ru-RU': () => import('antd/es/locale/ru_RU'),
};

// Undefined keeps antd's built-in en_US.
let antdLocale: Locale | undefined;

export async function loadAntdLocale(language: string): Promise<void> {
  antdLocale = (await antdLocaleLoaders[language]?.())?.default;
}

// A Form.Item tooltip icon sits inside its <label>, so its "question-circle" name
// became part of every hinted field's name. FormField exposes the hint as a description.
const FORM_CONFIG: ConfigProviderProps['form'] = {
  tooltip: { icon: <QuestionCircleOutlined aria-hidden /> },
};

export function AntdRootConfig({ children }: { children: ReactNode }) {
  return (
    <ConfigProvider locale={antdLocale} form={FORM_CONFIG}>
      {children}
    </ConfigProvider>
  );
}
