// Thin Radix Tabs wrapper that styles the trigger row to match the
// existing dashboard `.service-tabs` look (underlined active tab, muted
// inactive tabs).

import * as TabsPrimitive from "@radix-ui/react-tabs";
import clsx from "clsx";
import type {
  ComponentPropsWithoutRef,
  ElementRef,
  PropsWithChildren,
} from "react";
import { forwardRef } from "react";

export const Tabs = TabsPrimitive.Root;

type ListProps = ComponentPropsWithoutRef<typeof TabsPrimitive.List>;

export const TabsList = forwardRef<
  ElementRef<typeof TabsPrimitive.List>,
  ListProps
>(({ className, ...props }, ref) => (
  <TabsPrimitive.List
    ref={ref}
    className={clsx(
      "flex items-center gap-1 border-b border-line px-3",
      className,
    )}
    {...props}
  />
));
TabsList.displayName = "TabsList";

type TriggerProps = PropsWithChildren<
  ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>
>;

export const TabsTrigger = forwardRef<
  ElementRef<typeof TabsPrimitive.Trigger>,
  TriggerProps
>(({ className, children, ...props }, ref) => (
  <TabsPrimitive.Trigger
    ref={ref}
    className={clsx(
      "relative -mb-px cursor-pointer rounded-none border-0 border-b-2 border-transparent bg-transparent px-3 py-2 text-sm font-medium text-muted transition-colors",
      "hover:text-text",
      "data-[state=active]:border-accent data-[state=active]:text-text",
      "focus-visible:outline-none focus-visible:text-text",
      className,
    )}
    {...props}
  >
    {children}
  </TabsPrimitive.Trigger>
));
TabsTrigger.displayName = "TabsTrigger";

export const TabsContent = forwardRef<
  ElementRef<typeof TabsPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Content
    ref={ref}
    className={clsx("focus-visible:outline-none", className)}
    {...props}
  />
));
TabsContent.displayName = "TabsContent";
