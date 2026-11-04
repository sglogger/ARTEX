"use client";

import * as React from "react";

import Link from "next/link";

import { BriefcaseIcon, CheckIcon, ChevronsUpDownIcon, LayersIcon, PlusIcon, Settings2Icon } from "lucide-react";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { api } from "@/lib/api";
import { setActiveProjectId, useActiveProjectId } from "@/lib/project";
import type { Project } from "@/lib/types";

/** Picks the active project; every page lists only its tasks, chats, findings and assets. */
export function ProjectSwitcher() {
  const activeId = useActiveProjectId();
  const [projects, setProjects] = React.useState<Project[]>([]);

  // Reload the list whenever the selection changes, so new projects and counts show up.
  React.useEffect(() => {
    api
      .projects()
      .then((list) => {
        setProjects(list);
        // A project that no longer exists falls back to "all projects".
        if (activeId && !list.some((p) => String(p.id) === activeId)) setActiveProjectId("");
      })
      .catch(() => setProjects([]));
  }, [activeId]);

  const active = projects.find((p) => String(p.id) === activeId);
  let subtitle = "Everything, unfiltered";
  if (active) subtitle = active.client !== "" ? active.client : "Project";
  const open = projects.filter((p) => p.status === "active");
  const archived = projects.filter((p) => p.status === "archived");

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton size="lg" className="data-[state=open]:bg-sidebar-accent" tooltip="Project">
              <div className="flex size-8 shrink-0 items-center justify-center rounded-md bg-sidebar-primary text-sidebar-primary-foreground">
                {active ? <BriefcaseIcon className="size-4" /> : <LayersIcon className="size-4" />}
              </div>
              <div className="grid min-w-0 flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">{active ? active.name : "All projects"}</span>
                <span className="truncate text-muted-foreground text-xs">{subtitle}</span>
              </div>
              <ChevronsUpDownIcon className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-72" align="start" side="bottom">
            <DropdownMenuItem onSelect={() => setActiveProjectId("")}>
              <LayersIcon />
              <span className="flex-1">All projects</span>
              {!activeId && <CheckIcon className="size-4" />}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-muted-foreground text-xs">Projects</DropdownMenuLabel>
            {open.map((p) => (
              <DropdownMenuItem key={p.id} onSelect={() => setActiveProjectId(String(p.id))}>
                <BriefcaseIcon />
                <span className="min-w-0 flex-1 truncate">
                  {p.name}
                  {p.client && <span className="text-muted-foreground"> · {p.client}</span>}
                </span>
                {String(p.id) === activeId && <CheckIcon className="size-4" />}
              </DropdownMenuItem>
            ))}
            {archived.length > 0 && (
              <>
                <DropdownMenuLabel className="text-muted-foreground text-xs">Archived</DropdownMenuLabel>
                {archived.map((p) => (
                  <DropdownMenuItem key={p.id} onSelect={() => setActiveProjectId(String(p.id))}>
                    <BriefcaseIcon className="opacity-50" />
                    <span className="min-w-0 flex-1 truncate text-muted-foreground">{p.name}</span>
                    {String(p.id) === activeId && <CheckIcon className="size-4" />}
                  </DropdownMenuItem>
                ))}
              </>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <Link prefetch={false} href="/function/projects?new=1">
                <PlusIcon /> New project
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link prefetch={false} href="/function/projects">
                <Settings2Icon /> Manage projects
              </Link>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
