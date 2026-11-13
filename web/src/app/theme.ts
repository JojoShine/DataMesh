import type { ThemeConfig } from "antd";

const colorPrimary = "#1677ff";
const colorText = "#102349";

// Shared across the public portal, login and all four workbenches.
export const appTheme: ThemeConfig = {
  token: {
    colorPrimary,
    colorText,
    colorTextSecondary: "#60749a",
    colorBgLayout: "#f3f8fe",
    colorBorder: "#dce6f3",
    colorBorderSecondary: "#e5edf7",
    fontFamily: '"PingFang SC", "Microsoft YaHei", system-ui, sans-serif',
  },
  components: {
    App: { fontSize: 15 },
    Space: { fontSize: 15 },
    Flex: { fontSize: 15 },
    Layout: { fontSize: 15, siderBg: "#001b33", triggerBg: "#001b33" },
    Button: { primaryShadow: "none", contentFontSize: 14, controlHeight: 32 },
    Input: { fontSize: 15, inputFontSize: 15, controlHeight: 36 },
    InputNumber: { fontSize: 15, inputFontSize: 15, controlHeight: 36 },
    // The component algorithm must retain the platform text color for flat and tree options.
    Select: { algorithm: true, colorText, fontSize: 15, lineHeight: 1.6, optionFontSize: 15, controlHeight: 36 },
    TreeSelect: { colorText, fontSize: 15, lineHeight: 1.6, controlHeight: 36, titleHeight: 32 },
    DatePicker: { fontSize: 15, inputFontSize: 15, controlHeight: 36 },
    Form: { fontSize: 15, labelFontSize: 15, labelHeight: 36 },
    Checkbox: { fontSize: 15 },
    Radio: { fontSize: 15 },
    Modal: { fontSize: 15, titleFontSize: 18 },
    Drawer: { fontSize: 15, fontSizeLG: 18 },
    Alert: { fontSize: 15, fontSizeLG: 15 },
    Typography: { fontSize: 15, fontSizeHeading1: 24, fontSizeHeading2: 24, fontSizeHeading3: 18, fontSizeHeading4: 18, fontSizeHeading5: 18 },
    Descriptions: { fontSize: 15, fontSizeLG: 18 },
    Card: { fontSize: 15, headerFontSize: 18 },
    Tag: { fontSizeSM: 13 },
    Badge: { fontSize: 13, textFontSize: 13, textFontSizeSM: 13 },
    Tabs: { titleFontSize: 14, itemColor: colorPrimary },
    Table: {
      fontSize: 14, cellFontSize: 14,
      headerBg: "#f1f5fa", headerColor: "#455b7c",
    },
    Menu: { darkItemBg: "#001b33", darkItemSelectedBg: "#1677ff", darkItemColor: "#c8d5e7" },
  },
};
